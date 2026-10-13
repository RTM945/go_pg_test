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
	"net/http"
	"strings"
	"testing"

	"go_pg_test/app/test/msg"
	"go_pg_test/io"
)

const liveAddr = "http://127.0.0.1:8080"

// uid 现在传不了——服务器那边硬编码 1003，见 main.go 里"从 header 的 token 取 uid"那条 todo。

// 领红包。
func Test_CRedEnvelopeClick(t *testing.T) {
	// ↓↓↓ 填参数
	actId := int32(42)
	// ↑↑↑ 填参数

	req := msg.NewCRedEnvelopeClick()
	req.ActId = actId

	resp := msg.NewSRedEnvelope()
	roundTrip(t, req, resp)

	t.Logf("%v", resp)
}

// 上线/刷新。
func Test_CRedEnvelope(t *testing.T) {
	// ↓↓↓ 填参数
	actId := int32(42)
	// ↑↑↑ 填参数

	req := msg.NewCRedEnvelope()
	req.ActId = actId

	resp := msg.NewSRedEnvelope()
	roundTrip(t, req, resp)

	t.Logf("%v", resp)
}

// roundTrip 按封包格式发一帧、读回一帧解进 resp，两个方向的字节都打出来。
func roundTrip(t *testing.T, req, resp io.Msg) {
	t.Helper()

	payload := marshal(t, req)
	var frame bytes.Buffer
	if err := io.WriteFrame(&frame, req.TypeId(), payload); err != nil {
		t.Fatalf("拼帧失败: %v", err)
	}

	t.Logf("→ %T typeId=%d payload=%s", req, req.TypeId(), hex.EncodeToString(payload))

	httpResp, err := http.Post(liveAddr+"/", "application/octet-stream", &frame)
	if err != nil {
		t.Fatalf("发不出去，服务器起了吗（%s）: %v", liveAddr, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(httpResp.Body)
		t.Fatalf("HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(buf.String()))
	}

	respTypeId, respPayload, err := io.ReadFrame(httpResp.Body)
	if err != nil {
		t.Fatalf("响应的帧读不出来: %v", err)
	}
	if err := resp.Read(respPayload); err != nil {
		t.Fatalf("响应解不出 %T: %v，字节 %s", resp, err, hex.EncodeToString(respPayload))
	}

	t.Logf("← %T typeId=%d payload=%s", resp, respTypeId, hex.EncodeToString(respPayload))
}

func marshal(t *testing.T, m io.Msg) []byte {
	t.Helper()

	buf := make([]byte, m.Size())
	n, err := m.Write(buf)
	if err != nil {
		t.Fatalf("编码 %T 失败: %v", m, err)
	}
	return buf[:n]
}
