package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"go_pg_test/app/test/logic/mail"
	appsession "go_pg_test/app/test/session"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/httpserver"
	"go_pg_test/common/redis"
	"go_pg_test/io"
	"pdbgen/dbpool"
)

const (
	hdrReqToken   = "X-Request-Token" // 请求和响应都用它：请求带当前 token，响应带下一个
	hdrLoginToken = "X-Login-Token"   // 登录 / 免登录成功时响应下发
	hdrErrCode    = "X-Error-Code"    // 客户端靠它区分同一状态码下的不同错误
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

// handler 把传输、认证、token、错误映射都放在业务层：
// 读帧 → 验 loginToken → dispatch（内含事务和 requestToken 链）→ Flush。
func handler(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := appsession.New(httpserver.NewSession(w, log), r.Header.Get(hdrReqToken))

		typeId, payload, err := io.ReadFrame(r.Body)
		if err != nil {
			// 帧都读不出来，一定是客户端的问题。
			// 这里必须留痕：静默拒掉的话，日志里完全看不出"有请求来过"，
			// 排查时就只能靠猜（客户端协议对不上是开发期最常见的情况）。
			log.Warn("Decode frame failed", "remote", r.RemoteAddr, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if mode, ok := processModes[typeId]; ok && mode != processNone {
			uid, err := redis.VerifyLoginToken(r.Context(), bearer(r))
			switch {
			case err == nil:
				s.SetUID(uid)
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
		}

		if err := processRegistry.Dispatch(r.Context(), typeId, payload, s); err != nil {
			switch {
			// 协议号不认识、协议体解不出，都是客户端发错了
			case errors.Is(err, dispatch.ErrUnknownTypeId), errors.Is(err, dispatch.ErrMalformedPayload):
				log.Warn("Wrong typeId", "typeId", typeId, "remote", r.RemoteAddr, "err", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
			// 客户端手里的 requestToken 已过期/被消费：上一次成功响应可能丢了。
			// 客户端不要重放原请求，应静默免登录并重新同步状态。
			case errors.Is(err, ErrStaleRequestToken):
				writeErr(w, http.StatusUnauthorized, "REQ_TOKEN_STALE", "request token is stale")
			// Redis 快速去重：同一个 token 已经在处理/刚处理完。
			// 客户端的处理方式同上：不要重放，按需重新登录/同步。
			case errors.Is(err, ErrDuplicateRequest):
				writeErr(w, http.StatusConflict, "REQ_DUP", "duplicate request")
			case errors.Is(err, redis.ErrInvalidRequestToken):
				writeErr(w, http.StatusBadRequest, "REQ_TOKEN_INVALID", "invalid request token")
			case errors.Is(err, mail.ErrMailNotFound):
				writeErr(w, http.StatusNotFound, "MAIL_NOT_FOUND", "mail not found")
			// Redis 故障：幂等检查做不了，fail closed，让客户端稍后重试
			case errors.Is(err, redis.ErrRedisUnavailable):
				log.Error("request token check failed", "typeId", typeId, "uid", s.UID(), "err", err)
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusServiceUnavailable, "BUSY", "service busy")
			// 同一个 uid 的上一次请求还占着咨询锁。这不是服务端的错，
			// 语义是"你上一次还没做完，稍后重试"——回 500 的话客户端会当成 bug。
			case errors.Is(err, dbpool.ErrAdvisoryLockNotAcquired):
				log.Info("previous request for this user is still in flight", "typeId", typeId, "uid", s.UID())
				http.Error(w, "previous request for this user is still in flight", http.StatusConflict)
			// 剩下的才是服务端自己的问题，不把内部细节漏给客户端，但要留日志
			default:
				log.Error("dispatch failed", "typeId", typeId, "err", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
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
			return
		}
	}
}
