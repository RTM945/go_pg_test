package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go_pg_test/app/test/msg"
	"go_pg_test/common/dispatch"
	"go_pg_test/common/redis"
	"go_pg_test/io"
)

var discard = slog.New(slog.DiscardHandler)

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if recover() == nil {
			t.Fatal("应该 panic")
		}
	}()
	fn()
}

// serve 把一帧发给 handler，返回响应。
func serve(t *testing.T, typeId int32, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	var frame bytes.Buffer
	if err := io.WriteFrame(&frame, typeId, payload); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler(discard).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", &frame))
	return w
}

// TestEveryKindHasStatus 每个错误类别都要有 HTTP 状态码。
func TestEveryKindHasStatus(t *testing.T) {
	for k := dispatch.BadRequest; k.Valid(); k++ {
		if statusOf[k] == 0 {
			t.Errorf("dispatch.Kind %d 没有对应的 HTTP 状态码", k)
		}
	}
}

// TestWriteError 失败响应只有一个出口：ClientError 按声明返回，其他一律 500，细节不漏给客户端。
func TestWriteError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		status     int
		code       string
		show, hide string
		retryAfter bool
	}{
		{"客户端错误带上细节", fmt.Errorf("%w: typeId 7", dispatch.ErrMalformedPayload),
			http.StatusBadRequest, "BAD_PAYLOAD", "typeId 7", "", false},
		{"依赖故障不漏细节", fmt.Errorf("%w: dial tcp 10.0.0.1:6379", redis.ErrRedisUnavailable),
			http.StatusServiceUnavailable, "BUSY", "redis unavailable", "10.0.0.1", true},
		{"没声明契约的错误一律 500", errors.New("pq: secret detail"),
			http.StatusInternalServerError, "INTERNAL", "Internal Server Error", "secret detail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, discard, 1, 2, tc.err)

			body := w.Body.String()
			switch {
			case w.Code != tc.status || w.Header().Get(hdrErrCode) != tc.code:
				t.Fatalf("期望 %d %s，实际 %d %s", tc.status, tc.code, w.Code, w.Header().Get(hdrErrCode))
			case !strings.Contains(body, tc.show) || tc.hide != "" && strings.Contains(body, tc.hide):
				t.Fatalf("响应体不对: %q", body)
			case (w.Header().Get("Retry-After") != "") != tc.retryAfter:
				t.Fatalf("Retry-After 不对: %q", w.Header().Get("Retry-After"))
			}
		})
	}
}

// TestHandlerRecoversPanic 业务 panic 回 500 INTERNAL，而不是让 net/http 掐断连接。
func TestHandlerRecoversPanic(t *testing.T) {
	const typeId int32 = 9001
	register(processRegistry, typeId, msg.NewHeartbeat, func(context.Context, *msg.Heartbeat) error {
		panic("boom")
	}, processNone)

	if w := serve(t, typeId, nil); w.Code != http.StatusInternalServerError || w.Header().Get(hdrErrCode) != "INTERNAL" {
		t.Fatalf("期望 500 INTERNAL，实际 %d %s", w.Code, w.Header().Get(hdrErrCode))
	}
}

// TestHandlerRejectsBadRequests 坏帧、没注册的协议号都是 400，并且带错误码。
func TestHandlerRejectsBadRequests(t *testing.T) {
	w := httptest.NewRecorder()
	handler(discard).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("xx")))
	if w.Code != http.StatusBadRequest || w.Header().Get(hdrErrCode) != "BAD_FRAME" {
		t.Fatalf("坏帧期望 400 BAD_FRAME，实际 %d %s", w.Code, w.Header().Get(hdrErrCode))
	}

	if w := serve(t, 9999, nil); w.Code != http.StatusBadRequest || w.Header().Get(hdrErrCode) != "UNKNOWN_TYPE" {
		t.Fatalf("未知协议号期望 400 UNKNOWN_TYPE，实际 %d %s", w.Code, w.Header().Get(hdrErrCode))
	}
}

// TestRegisterRejectsMistakes 模式不认识、协议号登记两次，启动时就 panic。
func TestRegisterRejectsMistakes(t *testing.T) {
	reg := dispatch.NewProcessorRegistry()
	noop := func(context.Context, *msg.Heartbeat) error { return nil }

	mustPanic(t, func() { register(reg, 9100, msg.NewHeartbeat, noop, processMode(99)) })
	register(reg, 9101, msg.NewHeartbeat, noop, processNone)
	mustPanic(t, func() { register(reg, 9101, msg.NewHeartbeat, noop, processNone) })
}
