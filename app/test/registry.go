package main

import (
	"context"
	"fmt"

	"go_pg_test/app/test/ptable"
	appsession "go_pg_test/app/test/session"
	"go_pg_test/common/db"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/redis"
	"go_pg_test/common/token"
	"go_pg_test/io"
	"pdbgen/dbpool"
)

var (
	// ErrStaleRequestToken 客户端带来的 requestToken 和数据库里的不一致：上一次成功响应可能丢了。
	// 客户端不要重放原请求，应静默免登录并重新同步状态。
	ErrStaleRequestToken = dispatch.NewClientError(dispatch.Unauthorized, "REQ_TOKEN_STALE", "request token is stale")
	// ErrDuplicateRequest Redis 快速去重判定这个 token 已经在处理/处理过了。处理方式同上。
	ErrDuplicateRequest = dispatch.NewClientError(dispatch.Conflict, "REQ_DUP", "duplicate request")
)

var processRegistry = dispatch.NewProcessorRegistry()

// processModes 记每个协议怎么跑事务；handler 用它判断要不要验 loginToken。
var processModes = map[int32]processMode{}

type processMode int

const (
	processQuery       processMode = iota // 认证 + 只读，dbpool.Query
	processUserTryLock                    // 认证 + uid try 锁事务 + requestToken 链（玩家请求）
	processUserLock                       // 认证 + uid 阻塞锁事务 + requestToken 链
	processAuthOnly                       // 只验 loginToken（顺带续 Redis TTL），不开事务、不消费 requestToken
	processNone                           // 不认证、不开事务，逻辑自己管理（登录）
)

// register 把“反序列化 + 事务/锁 + requestToken 链”包进一个 DispatchFunc，
// 再登记进纯分发的 ProcessorRegistry。生成器只负责调用这个函数。
// 同一个协议号登记两次、或者模式不认识，启动时直接 panic。
func register[T io.Msg](
	reg *dispatch.ProcessorRegistry,
	typeId int32,
	newMsg func() T,
	fn func(ctx context.Context, req T) error,
	mode processMode,
) {
	if mode < processQuery || mode > processNone {
		panic(fmt.Sprintf("register typeId %d: unknown process mode %d", typeId, mode))
	}

	reg.Register(typeId, func(ctx context.Context, typeId int32, buffer []byte, s io.Session) error {
		msg := newMsg()
		if err := msg.Read(buffer); err != nil {
			return fmt.Errorf("%w: typeId %d: %w", dispatch.ErrMalformedPayload, typeId, err)
		}
		s.Logger().Debug("recv", "typeId", typeId, "msg", msg)
		msg.SetSession(s)

		process := func(ctx context.Context) error {
			return fn(ctx, msg)
		}

		// ctx 是请求的 ctx：客户端断开时还没提交的事务直接回滚（COMMIT 本身不受影响）。
		switch mode {
		case processQuery:
			return dbpool.Query(ctx, process)
		case processUserTryLock:
			return runUserWithRequestToken(ctx, s, db.WithUserTryLock, process)
		case processUserLock:
			return runUserWithRequestToken(ctx, s, db.WithUserLock, process)
		default: // processAuthOnly 的认证已经在 handler 里做完；processNone 不认证
			return process(ctx)
		}
	})
	processModes[typeId] = mode
}

// runUserWithRequestToken 先用 Redis SETNX 做快速去重，再进 DB 事务做权威比对。
//
// - 同一个 token 已经在处理/刚处理完：事务外直接拒绝，不拿 uid 锁；
// - 事务失败：释放 key，允许客户端用同一个 token 重试；
// - 事务成功：保留 key 到 TTL 过期，紧跟着的重复请求会被快速拒绝。
func runUserWithRequestToken(
	ctx context.Context,
	s io.Session,
	lock func(context.Context, int64, func(context.Context) error) error,
	process func(context.Context) error,
) error {
	appSession, ok := s.(*appsession.Session)
	if !ok {
		return fmt.Errorf("unexpected session type %T", s)
	}

	acquired, err := redis.TryAcquireRequest(ctx, s.UID(), appSession.ReqToken())
	if err != nil {
		return err
	}
	if !acquired {
		return ErrDuplicateRequest
	}

	committed := false
	defer func() {
		if !committed {
			_ = redis.ReleaseRequest(context.Background(), s.UID(), appSession.ReqToken())
		}
	}()

	if err := lock(ctx, s.UID(), func(ctx context.Context) error {
		return runUserProcess(ctx, s, process)
	}); err != nil {
		return err
	}
	committed = true
	return nil
}

// runUserProcess 在用户事务内维护 requestToken 单次链：
// 比对 → 业务 → 换新 token 并写响应头。
//
// token 和 user 同一行：同一事务里业务再 GetById 会拿到同一个对象，
// token 轮换和业务修改在提交时合并成一条 UPDATE。
func runUserProcess(ctx context.Context, s io.Session, process func(context.Context) error) error {
	appSession, ok := s.(*appsession.Session)
	if !ok {
		return fmt.Errorf("unexpected session type %T", s)
	}

	user := ptable.UserTable.GetById(ctx, s.UID())
	if user == nil || user.RequestToken() != appSession.ReqToken() {
		return ErrStaleRequestToken
	}

	if err := process(ctx); err != nil {
		return err
	}

	newToken := token.New32()
	user.SetRequestToken(newToken)
	appSession.SetNextReqToken(newToken)
	return nil
}
