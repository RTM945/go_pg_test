package httpserver

import (
	"context"
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
	for _, msg := range msgs {
		var err error
		if buf, err = io.AppendMsg(buf, msg); err != nil {
			return err
		}
	}
	s.logSend(msgs)

	if _, err := s.w.Write(buf); err != nil {
		return fmt.Errorf("flush %d frames: %w", len(msgs), err)
	}
	return nil
}

// logSend 打出这次发了哪些协议。格式化消息要走反射，比编码本身贵十几倍
// （带 20 封邮件的 SMails 要几十微秒），所以只在 debug 打开时才拼。
// 多帧时按 batch 展开，和 sql batch 一样一眼能看出这次发了什么。
func (s *Session) logSend(msgs []io.Msg) {
	log := s.Logger()
	if !log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if len(msgs) == 1 {
		log.Debug("send", "typeId", msgs[0].TypeId(), "msg", msgs[0])
		return
	}
	frames := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		frames = append(frames, fmt.Sprintf("typeId=%d msg=%v", msg.TypeId(), msg))
	}
	log.Debug("send batch", "count", len(frames), "frames", frames)
}
