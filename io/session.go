package io

import (
	"fmt"
	"log/slog"
	"net/http"
)

type Session struct {
	UID int64
	W   http.ResponseWriter

	// Log 这次请求的 logger，由服务端在建立 Session 时注入（通常已经 With 了 uid）。
	//
	// 放这里是因为生成的 handler 签名是 ProcessXxx(ctx, req) 定死的，加不了
	// logger 参数；而 Session 是每个请求天然都有的句柄。业务层想记日志就用它，
	// 别去调 slog 的全局函数——那样就写死成全局那一个 logger 了。
	Log *slog.Logger
}

// Logger 拿到这次请求的 logger，没注入的话回落成丢弃式的，
// 免得业务层为了防 nil 到处写判断。
func (session *Session) Logger() *slog.Logger {
	if session.Log != nil {
		return session.Log
	}
	return discardLogger
}

var discardLogger = slog.New(slog.DiscardHandler)

// Write 把 msg 编好，按封包格式写回客户端。封包格式本身在 WriteFrame 里。
func (session *Session) Write[T Msg](msg T) error {
	payload := make([]byte, msg.Size())
	n, err := msg.Write(payload)
	if err != nil {
		return fmt.Errorf("encode typeId %d: %w", msg.TypeId(), err)
	}
	if n != len(payload) {
		// Size() 和实际编出来的长度对不上，发出去的帧会是坏的，宁可在这里炸
		return fmt.Errorf("encode typeId %d: wrote %d bytes but Size() says %d", msg.TypeId(), n, len(payload))
	}
	return WriteFrame(session.W, msg.TypeId(), payload)
}
