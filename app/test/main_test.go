package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go_pg_test/app/test/msg"
	testpb "go_pg_test/genpb/test"
	"go_pg_test/io"
)

// encodeFrame 按封包格式拼一帧，测试里当客户端用。
// 格式本身由 io.WriteFrame 保证，也由 io/frame_test.go 里那份独立实现钉住。
func encodeFrame(typeId int32, payload []byte) []byte {
	var buf bytes.Buffer
	// 只有写 bytes.Buffer 出错或 payload 超限才会失败，测试里都不会发生
	if err := io.WriteFrame(&buf, typeId, payload); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// postFrame 把 body 当一帧丢给 handler，返回响应。
// 处理器走不到 db，只覆盖解析失败的分支。
func postFrame(t *testing.T, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	reg := NewProcessorRegistry()
	noop := func(_ context.Context, _ *msg.CRedEnvelope) error { return nil }
	reg.Register(testpb.PROTOCOL_TYPE_CRedEnvelope, msg.NewCRedEnvelope, noop, processUserTryLock)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(reg)(w, req)
	return w
}

// sendFrame 发一帧给 handler，reg 由调用方决定注册了什么。
func sendFrame(t *testing.T, reg *ProcessorRegistry, typeId int32, payload []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(encodeFrame(typeId, payload)))
	w := httptest.NewRecorder()
	handler(reg)(w, req)
	return w
}

// decodeSRedEnvelope 把 200 响应解回 SRedEnvelope，顺带校验帧格式和响应协议号。
// 返回指针——pb 结构体里带 sync.Mutex，按值返回会被 vet 拦。
func decodeSRedEnvelope(t *testing.T, w *httptest.ResponseRecorder) *testpb.SRedEnvelope {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", w.Code, w.Body.String())
	}
	typeId, payload, err := io.ReadFrame(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应的帧读不出来: %v，字节是 %x", err, w.Body.Bytes())
	}
	if typeId != testpb.PROTOCOL_TYPE_SRedEnvelope {
		t.Errorf("响应 typeId = %d, want %d", typeId, testpb.PROTOCOL_TYPE_SRedEnvelope)
	}

	resp := &testpb.SRedEnvelope{}
	if err := resp.UnmarshalVT(payload); err != nil {
		t.Fatalf("响应解不出 SRedEnvelope: %v", err)
	}
	return resp
}

func TestHandlerRejectsBadRequest(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"空 body", nil},
		{"包头不足", encodeFrame(testpb.PROTOCOL_TYPE_CRedEnvelope, []byte{1})[:5]},
		{"未注册的协议号", encodeFrame(999, []byte{0x08, 0x2a})},
		{"协议体不是合法 protobuf", encodeFrame(testpb.PROTOCOL_TYPE_CRedEnvelope, bytes.Repeat([]byte{0xff}, 11))},
		{"帧后有多余字节", append(encodeFrame(testpb.PROTOCOL_TYPE_CRedEnvelope, []byte{0x08, 0x2a}), 0xff)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postFrame(t, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d, body = %q", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

// 成功路径：处理器不碰 db，把整条链路走完——解帧 → 分发 → 处理器 → 写响应帧。
// 没有这条的话所有用例都在进处理器之前就 400 了，"能成功"没人测。
func TestHandlerWritesResponse(t *testing.T) {
	reg := NewProcessorRegistry()
	// 用 processQuery 是因为它不碰连接池——processUserTryLock 会去拿咨询锁，
	// 没有 db 直接 panic。这条测的是帧和响应，不是 db 模式。
	reg.Register(testpb.PROTOCOL_TYPE_CRedEnvelope, msg.NewCRedEnvelope,
		func(_ context.Context, req *msg.CRedEnvelope) error {
			resp := msg.NewSRedEnvelope()
			resp.ActId = req.GetActId()
			return req.Session().Write(resp)
		}, processQuery)

	resp := decodeSRedEnvelope(t, sendFrame(t, reg, testpb.PROTOCOL_TYPE_CRedEnvelope, []byte{0x08, 0x2a}))
	if resp.GetActId() != 42 {
		t.Errorf("响应的 actId = %d, want 42", resp.GetActId())
	}
}

// click 协议：两个协议同时注册时各走各的处理器不会串，响应带上更新后的次数。
func TestHandlerClickRequest(t *testing.T) {
	reg := NewProcessorRegistry()

	// 对面那条路，一旦被调用就说明派发串了
	var onlineCalled bool
	reg.Register(testpb.PROTOCOL_TYPE_CRedEnvelope, msg.NewCRedEnvelope,
		func(_ context.Context, _ *msg.CRedEnvelope) error {
			onlineCalled = true
			return nil
		}, processQuery)

	reg.Register(testpb.PROTOCOL_TYPE_CRedEnvelopeClick, msg.NewCRedEnvelopeClick,
		func(_ context.Context, req *msg.CRedEnvelopeClick) error {
			resp := msg.NewSRedEnvelope()
			resp.ActId = req.GetActId()
			resp.TodayCount = 1
			resp.Total = 1
			return req.Session().Write(resp)
		}, processQuery)

	resp := decodeSRedEnvelope(t, sendFrame(t, reg, testpb.PROTOCOL_TYPE_CRedEnvelopeClick, []byte{0x08, 0x2a}))

	if onlineCalled {
		t.Error("click 请求被派发到了 CRedEnvelope 的处理器")
	}
	if resp.GetActId() != 42 {
		t.Errorf("响应的 actId = %d, want 42", resp.GetActId())
	}
	if resp.GetTodayCount() != 1 || resp.GetTotal() != 1 {
		t.Errorf("todayCount=%d total=%d, want 1/1", resp.GetTodayCount(), resp.GetTotal())
	}
}

func TestHandlerNotFound(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/favicon.ico", nil)
	w := httptest.NewRecorder()
	handler(NewProcessorRegistry())(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
