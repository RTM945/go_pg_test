package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"

	"go_pg_test/io"
)

// Session 是 HTTP 传输下的 io.Session 实现。
//
// 它内部持有 http.ResponseWriter，但只用于 Flush；
// 业务层拿不到底层 writer，也就不会把 HTTP 语义带进业务。
type Session struct {
	w       http.ResponseWriter
	log     *slog.Logger
	uid     int64
	pending []io.Msg
}

func NewSession(w http.ResponseWriter, log *slog.Logger) *Session {
	return &Session{w: w, log: log}
}

// Logger 拿到这次请求的 logger，没注入的话回落成丢弃式的，
// 免得业务层为了防 nil 到处写判断。
func (s *Session) Logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return discardLogger
}

var discardLogger = slog.New(slog.DiscardHandler)

func (s *Session) UID() int64       { return s.uid }
func (s *Session) SetUID(uid int64) { s.uid = uid }

// Write 只把 msg 排队，真正的编码推迟到 Flush。
//
// 这样 Flush 时能打出最终发到网络的协议和字节；入队之后不要再改 msg，
// 否则发出去的内容和写的时候看到的不一样。
func (s *Session) Write(msg io.Msg) error {
	if msg == nil {
		return fmt.Errorf("nil msg")
	}
	s.pending = append(s.pending, msg)
	return nil
}

// PendingFrames 攒了几帧还没发。日志用。
func (s *Session) PendingFrames() int { return len(s.pending) }

// Flush 编码并写出攒下的消息。由 handler 在**事务提交成功之后**调。
//
// 提交失败时不该调它——那样客户端收到的是错误状态码，而不是一个
// 业务已经回滚了的"成功"响应。
func (s *Session) Flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	msgs := s.pending
	s.pending = nil

	var buf []byte
	frames := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		payload := make([]byte, msg.Size())
		n, err := msg.Write(payload)
		if err != nil {
			return fmt.Errorf("encode typeId %d: %w", msg.TypeId(), err)
		}
		if n != len(payload) {
			// Size() 和实际编出来的长度对不上，发出去的帧会是坏的，宁可不发
			return fmt.Errorf("encode typeId %d: wrote %d bytes but Size() says %d", msg.TypeId(), n, len(payload))
		}
		payload = payload[:n]

		// 经过 slog 的 Resolve：消息实现了 LogValuer 时用它（sensitive 字段打成 "***"）
		frames = append(frames, fmt.Sprintf("typeId=%d msg=%s", msg.TypeId(), slog.AnyValue(msg).Resolve()))
		if buf, err = io.AppendFrame(buf, msg.TypeId(), payload); err != nil {
			return err
		}
	}

	// 下发多帧时按 batch 日志展开，和 sql batch 一样一眼能看出这次发了什么。
	if len(frames) == 1 {
		s.Logger().Debug("send", "typeId", msgs[0].TypeId(), "msg", msgs[0])
	} else {
		s.Logger().Debug("send batch", "count", len(frames), "frames", frames)
	}

	if _, err := s.w.Write(buf); err != nil {
		return fmt.Errorf("flush %d frames: %w", len(msgs), err)
	}
	return nil
}
