package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go_pg_test/app/test/logic/event"
	"go_pg_test/app/test/logic/mail"
	"go_pg_test/common/redis"
)

const hdrGMToken = "X-GM-Token"

type gmMailRequest struct {
	UUID          string `json:"uuid"`
	Params        string `json:"params"`
	AwardList     string `json:"awardList"`
	StartAt       int64  `json:"startAt"`
	EndAt         int64  `json:"endAt"`
	Channel       string `json:"channel"`
	UserCreatedAt int64  `json:"userCreatedAt"`
	Condition     string `json:"condition"`
}

func gmAuthorized(r *http.Request, token string) bool {
	got := r.Header.Get(hdrGMToken)
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// gmMailHandler 接收 GM 平台广播。所有服收到同一个 uuid，各自幂等写库并
// 刷新自己的 gm_mail 缓存；成功返回后平台再调 notify 通知客户端。
func gmMailHandler(log *slog.Logger, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gmAuthorized(r, token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req gmMailRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}

		id, created, err := mail.PublishGlobal(r.Context(), req.UUID, mail.GlobalMail{
			Params:        req.Params,
			AwardList:     req.AwardList,
			StartAt:       req.StartAt,
			EndAt:         req.EndAt,
			Channel:       req.Channel,
			UserCreatedAt: req.UserCreatedAt,
			Condition:     req.Condition,
		})
		if err != nil {
			if errors.Is(err, mail.ErrInvalidMail) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			log.Error("publish gm mail", "uuid", req.UUID, "err", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "created": created})
	}
}

// gmMailNotifyHandler 在所有服都写完并刷新缓存后调用，只 bump 一次全局事件。
func gmMailNotifyHandler(log *slog.Logger, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gmAuthorized(r, token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		seq, err := redis.IncrGlobalEventVersion(r.Context(), event.EventTypeMailNew)
		if err != nil {
			log.Error("notify gm mail", "err", err)
			http.Error(w, "notify failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"eventSeq": seq})
	}
}
