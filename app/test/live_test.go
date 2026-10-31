//go:build live

// 实机联调：手动填参数，打给跑着的服务器，把响应打出来。
//
// 需要服务器先起来（默认 127.0.0.1:8080，改 liveAddr）。
//
// 命令行单个跑：
//
//	go test -tags=live ./app/test -run Test_CRedEnvelopeClick -v
//
// GoLand 里跑：Run Configuration 的 "Go tool arguments" 填 -tags=live，
// 之后就能点某个函数旁边的绿三角单独跑它。加 -v 才看得到 t.Log 的输出。

package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	testpb "go_pg_test/genpb/test"
	"go_pg_test/io"
)

const liveAddr = "http://127.0.0.1:8080"

// liveSession 整个测试进程只登录一次，后续请求复用这套 token。
var liveSession struct {
	ready      bool
	loginToken string
	reqToken   string
}

// ensureLogin 拿一个 test 平台的随机账号做 SDK 登录。
// 用户侧协议要走鉴权，所以 live test 不能再直接裸发请求。
func ensureLogin(t *testing.T) {
	t.Helper()
	if liveSession.ready {
		return
	}

	login := &testpb.CLogin{
		Plat:          "test",
		Account:       fmt.Sprintf("live-%d", time.Now().UnixNano()),
		ClientVersion: "live",
		Ext:           map[int32]string{5: "live-device"},
	}

	frame, err := io.AppendMsg(nil, login)
	if err != nil {
		t.Fatalf("拼登录帧失败: %v", err)
	}
	httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, liveAddr+"/", bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("登录失败，服务器起了吗（%s）: %v", liveAddr, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		var body bytes.Buffer
		_, _ = body.ReadFrom(httpResp.Body)
		t.Fatalf("登录 HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(body.String()))
	}
	liveSession.loginToken = httpResp.Header.Get("X-Login-Token")
	liveSession.reqToken = httpResp.Header.Get("X-Request-Token")
	if liveSession.loginToken == "" || liveSession.reqToken == "" {
		t.Fatalf("登录响应缺 token header: login=%q req=%q",
			liveSession.loginToken, liveSession.reqToken)
	}
	frames, err := io.ReadFrames(httpResp.Body)
	if err != nil {
		t.Fatalf("登录响应读不出来: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("登录响应没有 LoginResult")
	}
	result := &testpb.LoginResult{}
	if err := result.UnmarshalVT(frames[0].Payload); err != nil {
		t.Fatalf("LoginResult 解不出来: %v", err)
	}
	liveSession.ready = true
	t.Logf("login ok: uid=%d loginToken=%s reqToken=%s", result.Uid, liveSession.loginToken, liveSession.reqToken)
}

// 领红包。
func Test_CRedEnvelopeClick(t *testing.T) {
	// ↓↓↓ 填参数
	actId := int32(42)
	// ↑↑↑ 填参数

	resp := &testpb.SRedEnvelope{}
	roundTrip(t, &testpb.CRedEnvelopeClick{ActId: actId}, resp)

	t.Logf("%v", resp)
}

// 上线/刷新。
func Test_CRedEnvelope(t *testing.T) {
	// ↓↓↓ 填参数
	actId := int32(42)
	// ↑↑↑ 填参数

	resp := &testpb.SRedEnvelope{}
	roundTrip(t, &testpb.CRedEnvelope{ActId: actId}, resp)

	t.Logf("%v", resp)
}

// roundTrip 按封包格式发一帧、读回一帧解进 resp，两个方向的字节都打出来。
func roundTrip(t *testing.T, req, resp io.Msg) {
	t.Helper()
	ensureLogin(t)

	frame, err := io.AppendMsg(nil, req)
	if err != nil {
		t.Fatalf("拼帧失败: %v", err)
	}

	t.Logf("→ %T typeId=%d payload=%s", req, req.TypeId(), hex.EncodeToString(frame[io.FrameHeaderSize:]))

	httpReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, liveAddr+"/", bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpReq.Header.Set("Authorization", "Bearer "+liveSession.loginToken)
	httpReq.Header.Set("X-Request-Token", liveSession.reqToken)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("发不出去，服务器起了吗（%s）: %v", liveAddr, err)
	}
	defer httpResp.Body.Close()

	// 成功响应会下发下一个 requestToken，客户端必须接着用新的。
	if next := httpResp.Header.Get("X-Request-Token"); next != "" {
		liveSession.reqToken = next
	}

	if httpResp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(httpResp.Body)
		t.Fatalf("HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(buf.String()))
	}

	// 响应可能不止一帧（一次请求触发多个模块时服务端会一起发回来），
	// 客户端一律循环读到流尾，不为"批"单独走一条路。
	frames, err := io.ReadFrames(httpResp.Body)
	if err != nil {
		t.Fatalf("响应的帧读不出来: %v", err)
	}
	if len(frames) == 0 {
		t.Fatalf("服务器一帧都没回")
	}
	for i, f := range frames {
		t.Logf("← [%d/%d] typeId=%d payload=%s", i+1, len(frames), f.TypeId, hex.EncodeToString(f.Payload))
	}

	if err := resp.UnmarshalVT(frames[0].Payload); err != nil {
		t.Fatalf("响应解不出 %T: %v，字节 %s", resp, err, hex.EncodeToString(frames[0].Payload))
	}
	t.Logf("%v", resp)
}
