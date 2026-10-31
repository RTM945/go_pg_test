package session

import (
	"context"
	"fmt"

	"go_pg_test/app/test/ptable"
	"go_pg_test/common/db"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/redis"
	"go_pg_test/common/token"
	"go_pg_test/io"
)

// 处理方式（认证、事务、锁、requestToken 链）由处理函数 session 参数的类型决定：
//
//	func ProcessCMails(ctx context.Context, s *session.User, req *testpb.CMails) error
//
// 换处理方式就是换这个类型。生成的注册表只写 processRegistry.Handle(mail.ProcessCMails)，
// 不知道处理方式；协议（proto）里也不写——那是服务端怎么访问数据库的事。
//
// 要只读查询、阻塞锁这类新方式时，在这里加一种类型，实现 mode 的三个方法。

// Guest 不认证、不开事务，逻辑自己管（登录）。
type Guest struct{ *Session }

// Auth 只验 loginToken（顺带续 Redis TTL），不开事务、不消费 requestToken（心跳、拉事件版本）。
type Auth struct{ *Session }

// User 验 loginToken，在 uid try 锁事务里执行，并维护 requestToken 链：玩家请求的默认方式。
// 同一个 uid 的上一个请求还没完成时直接返回 db.ErrBusy。
type User struct{ *Session }

// mode 是一种处理方式。不导出：处理方式只能在这个包里定义。
type mode[S any] interface {
	*S
	bind(s *Session)
	// authenticate 在解码 payload 之前执行：没通过的请求不解码。
	authenticate(ctx context.Context) error
	// run 在这种方式要求的事务和锁里执行 fn。
	run(ctx context.Context, fn func(context.Context) error) error
}

func (m *Guest) bind(s *Session)                    { m.Session = s }
func (m *Guest) authenticate(context.Context) error { return nil }
func (m *Guest) run(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (m *Auth) bind(s *Session)                        { m.Session = s }
func (m *Auth) authenticate(ctx context.Context) error { return m.verifyLoginToken(ctx) }
func (m *Auth) run(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (m *User) bind(s *Session)                        { m.Session = s }
func (m *User) authenticate(ctx context.Context) error { return m.verifyLoginToken(ctx) }
func (m *User) run(ctx context.Context, fn func(context.Context) error) error {
	return m.withRequestToken(ctx, db.WithUserTryLock, fn)
}

// Registry 按协议号分发请求。
type Registry struct {
	*dispatch.ProcessorRegistry[*Session]
}

func NewRegistry() *Registry {
	return &Registry{dispatch.NewProcessorRegistry[*Session]()}
}

// Handle 登记一个协议的处理函数。协议号取自 req 的类型，处理方式取自 s 的类型，
// 两样都从 fn 的签名推出来，调用方只写 Handle(mail.ProcessCMails)。
// 同一个协议号登记两次直接 panic。
func (r *Registry) Handle[S any, PS mode[S], T any, PT interface {
	*T
	io.Msg
}](fn func(ctx context.Context, s PS, req PT) error) {
	var zero PT
	r.Register(zero.TypeId(), func(ctx context.Context, typeId int32, buffer []byte, s *Session) error {
		m := PS(new(S))
		m.bind(s)
		if err := m.authenticate(ctx); err != nil {
			return err
		}
		req := PT(new(T))
		if err := req.UnmarshalVT(buffer); err != nil {
			return fmt.Errorf("%w: typeId %d: %w", dispatch.ErrMalformedPayload, typeId, err)
		}
		s.Logger().Debug("recv", "typeId", typeId, "msg", req)
		// ctx 是请求的 ctx：客户端断开时还没提交的事务直接回滚（COMMIT 本身不受影响）。
		return m.run(ctx, func(ctx context.Context) error { return fn(ctx, m, req) })
	})
}

// verifyLoginToken 验 loginToken（顺带续 Redis TTL），通过了记下 uid。
func (s *Session) verifyLoginToken(ctx context.Context) error {
	uid, err := redis.VerifyLoginToken(ctx, s.loginToken)
	if err != nil {
		return err
	}
	s.SetUID(uid)
	return nil
}

var (
	// ErrStaleRequestToken 客户端带来的 requestToken 和数据库里的不一致：上一次成功响应可能丢了。
	// 客户端不要重放原请求，应静默免登录并重新同步状态。
	ErrStaleRequestToken = dispatch.NewClientError(dispatch.Unauthorized, "REQ_TOKEN_STALE", "request token is stale")
	// ErrDuplicateRequest Redis 快速去重判定这个 token 已经在处理/处理过了。处理方式同上。
	ErrDuplicateRequest = dispatch.NewClientError(dispatch.Conflict, "REQ_DUP", "duplicate request")
)

// withRequestToken 先用 Redis SETNX 做快速去重，再进 DB 事务做权威比对。
//
// - 同一个 token 已经在处理/刚处理完：事务外直接拒绝，不拿 uid 锁；
// - 事务失败：释放 key，允许客户端用同一个 token 重试；
// - 事务成功：保留 key 到 TTL 过期，紧跟着的重复请求会被快速拒绝。
func (s *Session) withRequestToken(
	ctx context.Context,
	lock func(context.Context, int64, func(context.Context) error) error,
	fn func(context.Context) error,
) error {
	acquired, err := redis.TryAcquireRequest(ctx, s.UID(), s.reqToken)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrDuplicateRequest
	}

	committed := false
	defer func() {
		if !committed {
			_ = redis.ReleaseRequest(context.Background(), s.UID(), s.reqToken)
		}
	}()

	if err := lock(ctx, s.UID(), func(ctx context.Context) error {
		return s.rotateRequestToken(ctx, fn)
	}); err != nil {
		return err
	}
	committed = true
	return nil
}

// rotateRequestToken 在用户事务内维护 requestToken 单次链：
// 比对 → 业务 → 换新 token 并写响应头。
//
// token 和 user 同一行：同一事务里业务再 GetById 会拿到同一个对象，
// token 轮换和业务修改在提交时合并成一条 UPDATE。
func (s *Session) rotateRequestToken(ctx context.Context, fn func(context.Context) error) error {
	user := ptable.UserTable.GetById(ctx, s.UID())
	if user == nil || user.RequestToken() != s.reqToken {
		return ErrStaleRequestToken
	}

	if err := fn(ctx); err != nil {
		return err
	}

	newToken := token.New32()
	user.SetRequestToken(newToken)
	s.SetNextReqToken(newToken)
	return nil
}
