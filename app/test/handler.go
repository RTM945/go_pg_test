package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	appsession "go_pg_test/app/test/session"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/httpserver"
	"go_pg_test/io"
)

const (
	hdrReqToken   = "X-Request-Token" // 请求和响应都用它：请求带当前 token，响应带下一个
	hdrLoginToken = "X-Login-Token"   // 登录 / 免登录成功时响应下发
	hdrErrCode    = "X-Error-Code"    // 每个失败响应都带：客户端靠它区分同一状态码下的不同错误
)

var errBadFrame = dispatch.NewClientError(dispatch.BadRequest, "BAD_FRAME", "malformed frame")

// statusOf 错误类别到 HTTP 状态码。每个 dispatch.Kind 都必须在这里（handler_test 检查）。
var statusOf = map[dispatch.Kind]int{
	dispatch.BadRequest:      http.StatusBadRequest,
	dispatch.Unauthorized:    http.StatusUnauthorized,
	dispatch.Forbidden:       http.StatusForbidden,
	dispatch.NotFound:        http.StatusNotFound,
	dispatch.Conflict:        http.StatusConflict,
	dispatch.UpgradeRequired: http.StatusUpgradeRequired,
	dispatch.Unavailable:     http.StatusServiceUnavailable,
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

// writeError 是失败响应的唯一出口：dispatch.ClientError 按它声明的类别和错误码返回；
// 其他错误都是服务端自己的问题，一律 500 INTERNAL，细节只进日志、不漏给客户端。
func writeError(w http.ResponseWriter, log *slog.Logger, typeId int32, uid int64, err error) {
	ce, ok := errors.AsType[*dispatch.ClientError](err)
	if !ok {
		log.Error("request failed", "typeId", typeId, "uid", uid, "err", err)
		w.Header().Set(hdrErrCode, "INTERNAL")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	body := err.Error()
	switch ce.Kind {
	case dispatch.Unavailable:
		// 服务端依赖出了问题（Redis、平台接口）：留日志；底层细节不发给客户端
		log.Error("request failed", "typeId", typeId, "uid", uid, "code", ce.Code, "err", err)
		w.Header().Set("Retry-After", "1")
		body = ce.Error()
	case dispatch.BadRequest:
		// 客户端发错了，开发期最常见的是协议对不上：必须留痕，否则日志里看不出有请求来过
		log.Warn("bad request", "typeId", typeId, "uid", uid, "code", ce.Code, "err", err)
	default:
		log.Info("request rejected", "typeId", typeId, "uid", uid, "code", ce.Code, "err", err)
	}
	w.Header().Set(hdrErrCode, ce.Code)
	http.Error(w, body, statusOf[ce.Kind])
}

// handler 把传输、token、错误映射都放在业务层：
// 读帧 → dispatch（按处理函数的 session 类型认证、开事务、维护 requestToken 链）→ Flush。
func handler(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := appsession.New(httpserver.NewSession(w, log), bearer(r), r.Header.Get(hdrReqToken))

		var typeId int32
		defer func() {
			// panic 是 pdbgen 和业务的错误传播方式，事务已经在 Procedure 里回滚。
			// 在这里收口：记堆栈、回 500，而不是让 net/http 掐断连接。
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.Error("request panic", "typeId", typeId, "uid", s.UID(), "panic", p, "stack", string(debug.Stack()))
				w.Header().Set(hdrErrCode, "INTERNAL")
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		typeId, payload, err := io.ReadFrame(r.Body)
		if err != nil {
			writeError(w, log, 0, 0, fmt.Errorf("%w: %w", errBadFrame, err))
			return
		}

		if err := processRegistry.Dispatch(r.Context(), typeId, payload, s); err != nil {
			writeError(w, log, typeId, s.UID(), err)
			return
		}

		// Dispatch 返回意味着事务已经提交，现在才发 token 和业务响应。
		if v := s.NextLoginToken(); v != "" {
			w.Header().Set(hdrLoginToken, v)
		}
		if v := s.NextReqToken(); v != "" {
			w.Header().Set(hdrReqToken, v)
		}

		frames := s.PendingFrames()
		if err := s.Flush(); err != nil {
			// 响应多半已经写出去了，状态码改不了，只能记一笔
			log.Error("flush response failed", "typeId", typeId, "frames", frames, "err", err)
		}
	}
}
