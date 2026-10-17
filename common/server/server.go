package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go_pg_test/io"
)

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = errors.New("unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf。
	ErrMalformedPayload = errors.New("malformed protocol payload")
	// ErrStaleRequestToken 客户端带来的 requestToken 和数据库里的不一致。
	// 具体怎么恢复由业务层决定（这里是免登录换一套新 token）。
	ErrStaleRequestToken = errors.New("stale request token")
)

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

// DispatchFunc 一次协议分发：typeId、原始 payload、Session 都已就绪。
type DispatchFunc func(ctx context.Context, typeId int32, buffer []byte, session io.Session) error

// ProcessorRegistry 只做 typeId -> handler 的登记和分发。
// 事务、锁、认证、token 都由业务层在注册 handler 时自己包。
type ProcessorRegistry struct {
	registry map[int32]DispatchFunc
}

func NewProcessorRegistry() *ProcessorRegistry {
	return &ProcessorRegistry{registry: make(map[int32]DispatchFunc)}
}

func (p *ProcessorRegistry) Register(typeId int32, fn DispatchFunc) {
	p.registry[typeId] = fn
}

func (p *ProcessorRegistry) Dispatch(ctx context.Context, typeId int32, buffer []byte, session io.Session) error {
	fn, ok := p.registry[typeId]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownTypeId, typeId)
	}
	return fn(ctx, typeId, buffer, session)
}
