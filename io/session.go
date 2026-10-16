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

	// pending 攒着还没发出去的帧（连续的，Flush 时一次写出）。
	//
	// 不立刻发有两个理由：
	//  1. 一次请求可能触发多个模块，各自的响应协议要一起发回客户端
	//  2. 业务跑在事务里，提交之前发出去的话，Commit 失败客户端就已经
	//     收到"成功"了。攒着，由 handler 在事务提交成功之后调 Flush。
	pending []byte
	settled int // pending 里已经有几帧
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

// Write 把 msg 编好、攒进本次请求的响应里。真正发出去是在 Flush。
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

	// 拼错了就整个请求作废：pending 不完整了，不能只丢这一帧接着往下发
	if session.pending, err = AppendFrame(session.pending, msg.TypeId(), payload); err != nil {
		return err
	}
	session.settled++
	return nil
}

// PendingFrames 攒了几帧还没发。日志用。
func (session *Session) PendingFrames() int { return session.settled }

// Flush 把攒下的帧一次写出。由 handler 在**事务提交成功之后**调。
//
// 提交失败时不该调它——那样客户端收到的是错误状态码，而不是一个
// 业务已经回滚了的"成功"响应。
func (session *Session) Flush() error {
	if len(session.pending) == 0 {
		return nil
	}
	frames := session.settled

	_, err := session.W.Write(session.pending)

	// 无论成败都清掉：失败时响应多半已经写出去了，重试只会让客户端
	// 收到两份。错误交回调用方去记日志。
	session.pending = session.pending[:0]
	session.settled = 0

	if err != nil {
		return fmt.Errorf("flush %d frames: %w", frames, err)
	}
	return nil
}
