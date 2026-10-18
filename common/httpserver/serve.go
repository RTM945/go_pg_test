package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Serve 起 HTTP 服务，ctx 一取消就优雅关闭：不再收新连接，等在途请求跑完。
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
