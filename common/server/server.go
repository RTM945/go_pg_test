package server

import (
	"context"
	"errors"
	"fmt"
	"go_pg_test/common/db"
	"go_pg_test/common/redis"
	"go_pg_test/io"
	"log/slog"
	"net/http"
	"pdbgen/dbpool"
	"strings"
	"time"
)

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = errors.New("unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf。
	ErrMalformedPayload = errors.New("malformed protocol payload")
)

const (
	hdrReqToken = "X-Request-Token"
	hdrErrCode  = "X-Error-Code" // 客户端靠它区分同一状态码下的不同错误
)

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set(hdrErrCode, code)
	http.Error(w, msg, status)
}

// Serve 起服务，ctx 一取消就优雅关闭：不再收新连接，等在途请求跑完。
//
// ListenAndServe 是阻塞的，所以它得在 goroutine 里跑，主流程留着等 ctx。
// Shutdown 的语义是"等"，它不会去取消在途请求的 ctx——所以这里的超时
// 不是用来打断请求的，是最后兜底：等不到就强关，并让这件事在日志里可见。
func Serve(ctx context.Context, srv *http.Server, timeout time.Duration, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		// 还没等到信号就退出了，多半是端口被占
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	case <-ctx.Done():
		log.Info("Receive shutdown signal, wait for shutdown to complete")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("Shutdown timeout, force close", "timeout", timeout, "err", err)
		_ = srv.Close()
	}
	return nil
}

func Handler(reg *ProcessorRegistry, log *slog.Logger) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {

		session := &io.Session{W: w, Log: log} // UID 先是 0

		var (
			uid      int64
			reqToken string
			acquired bool
			done     bool // Dispatch 成功 = 事务已提交
		)
		defer func() {
			if acquired && !done {
				_ = redis.ReleaseRequest(context.Background(), uid, reqToken)
			}
		}()

		// body 就是一帧二进制，解出协议号和协议体
		typeId, payload, err := io.ReadFrame(r.Body)
		if err != nil {
			// 帧都读不出来，一定是客户端的问题。
			// 这里必须留痕：静默拒掉的话，日志里完全看不出"有请求来过"，
			// 排查时就只能靠猜（客户端协议对不上是开发期最常见的情况）。
			log.Warn("Decode frame failed", "remote", r.RemoteAddr, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if reg.RequiresAuth(typeId) {
			var err error
			uid, err = redis.VerifyLoginToken(r.Context(), bearer(r))
			switch {
			case err == nil:
				session.UID = uid
			case errors.Is(err, redis.ErrTokenExpired):
				writeErr(w, http.StatusUnauthorized, "TOKEN_EXPIRED", "login token expired")
				return
			case errors.Is(err, redis.ErrKicked):
				writeErr(w, http.StatusUnauthorized, "KICKED", "logged in elsewhere")
				return
			default: // Redis 故障，不能说成过期，否则会引发免登录风暴
				log.Error("verify login token failed", "err", err)
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusServiceUnavailable, "BUSY", "service busy")
				return
			}

			reqToken = r.Header.Get(hdrReqToken)
			ok, err := redis.TryAcquireRequest(r.Context(), uid, reqToken)
			if err != nil {
				if errors.Is(err, redis.ErrInvalidRequestToken) {
					writeErr(w, http.StatusBadRequest, "REQ_TOKEN_INVALID", "invalid request token")
					return
				}
				log.Error("acquire request failed", "uid", uid, "err", err)
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusServiceUnavailable, "BUSY", "service busy")
				return
			}
			if !ok {
				writeErr(w, http.StatusConflict, "REQ_DUP", "duplicate request")
				return
			}
			acquired = true
		}

		if err := reg.Dispatch(typeId, payload, session); err != nil {
			// 协议号不认识、协议体解不出，都是客户端发错了
			if errors.Is(err, ErrUnknownTypeId) || errors.Is(err, ErrMalformedPayload) {
				log.Warn("Wrong typeId", "typeId", typeId, "remote", r.RemoteAddr, "err", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// 同一个 uid 的上一次请求还占着咨询锁。这不是服务端的错，
			// 语义是"你上一次还没做完，稍后重试"——回 500 的话客户端会当成 bug。
			if errors.Is(err, dbpool.ErrAdvisoryLockNotAcquired) {
				log.Info("previous request for this user is still in flight", "typeId", typeId, "uid", session.UID)
				http.Error(w, "previous request for this user is still in flight", http.StatusConflict)
				return
			}
			// 剩下的才是服务端自己的问题，不把内部细节漏给客户端，但要留日志
			log.Error("dispatch failed", "typeId", typeId, "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		done = true // 之后 Flush 失败也不释放：事务已提交，token 已换新

		// Dispatch 返回意味着事务已经提交（withTx 的 defer 在它返回之前跑完），
		// 现在才把业务攒下的响应发出去。
		//
		// 上面任何一条 error 分支都直接 return 了，不会走到这里——事务回滚了，
		// 攒的帧跟着丢掉，客户端收到的是错误状态码而不是一个已回滚的"成功"。
		frames := session.PendingFrames()
		if err := session.Flush(); err != nil {
			// 响应多半已经写出去了，状态码改不了，只能记一笔
			log.Error("flush response failed", "typeId", typeId, "frames", frames, "err", err)
			return
		}
		log.Debug("response sent", "typeId", typeId, "frames", frames)
	}
}

type processFunc[T io.Msg] func(ctx context.Context, req T) error

type dispatchFunc func(buffer []byte, session *io.Session) error

type processMode int

const (
	ProcessQuery processMode = iota
	ProcessUserTryLock
	ProcessUserLock
	ProcessNamedTryLock
	ProcessNamedLock
	ProcessNone // 不开事务不加锁，handler 自己管理
)

type ProcessorRegistry struct {
	registry map[int32]dispatchFunc
	modes    map[int32]processMode
}

func NewProcessorRegistry() *ProcessorRegistry {
	return &ProcessorRegistry{registry: make(map[int32]dispatchFunc)}
}

func (p *ProcessorRegistry) RequiresAuth(typeId int32) bool {
	m, ok := p.modes[typeId]
	return ok && m != ProcessNone // 未知协议号放行，交给 Dispatch 返回 ErrUnknownTypeId
}

func (p *ProcessorRegistry) Register[T io.Msg](typeId int32, newMsg func() T, fn processFunc[T], mode processMode) {
	p.registry[typeId] = func(buffer []byte, session *io.Session) error {
		msg := newMsg()
		if err := msg.Read(buffer); err != nil {
			return fmt.Errorf("%w: typeId %d: %w", ErrMalformedPayload, typeId, err)
		}
		msg.SetSession(session)

		process := func(ctx context.Context) error {
			return fn(ctx, msg)
		}

		// db!
		ctx := context.Background()
		switch mode {
		case ProcessQuery:
			return dbpool.Query(ctx, process)
		case ProcessUserTryLock:
			return db.WithUserTryLock(ctx, session.UID, process)
		case ProcessUserLock:
			return db.WithUserLock(ctx, session.UID, process)
		case ProcessNone:
			return process(ctx)
		default:
			// todo ProcessNamedTryLock 和 ProcessNamedLock 基本用不到
			return fmt.Errorf("unknown process mode %d for typeId %d", mode, typeId)
		}
	}
	p.modes[typeId] = mode
}

func (p *ProcessorRegistry) Dispatch(typeId int32, buffer []byte, session *io.Session) error {
	fn, ok := p.registry[typeId]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownTypeId, typeId)
	}
	return fn(buffer, session)
}
