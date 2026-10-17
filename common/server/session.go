package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"go_pg_test/io"
)

// Session 是一次请求的基础状态：怎么写响应、UID、logger。
//
// 业务层用嵌入扩展它，比如 app 的 session 会再挂 requestToken 之类的字段。
// 这里刻意不出现任何 token/认证概念。
type Session struct {
	w       http.ResponseWriter
	log     *slog.Logger
	uid     int64
	pending []byte
	settled int // pending 里已经有几帧
}

func NewSession(w http.ResponseWriter, log *slog.Logger) *Session {
	return &Session{w: w, log: log}
}

func (s *Session) Writer() http.ResponseWriter { return s.w }

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

// Write 把 msg 编好、攒进本次请求的响应里。真正发出去是在 Flush。
func (s *Session) Write(msg io.Msg) error {
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
	if s.pending, err = io.AppendFrame(s.pending, msg.TypeId(), payload); err != nil {
		return err
	}
	s.settled++
	return nil
}

// PendingFrames 攒了几帧还没发。日志用。
func (s *Session) PendingFrames() int { return s.settled }

// Flush 把攒下的帧一次写出。由 handler 在**事务提交成功之后**调。
//
// 提交失败时不该调它——那样客户端收到的是错误状态码，而不是一个
// 业务已经回滚了的"成功"响应。
func (s *Session) Flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	frames := s.settled

	_, err := s.w.Write(s.pending)

	// 无论成败都清掉：失败时响应多半已经写出去了，重试只会让客户端
	// 收到两份。错误交回调用方去记日志。
	s.pending = s.pending[:0]
	s.settled = 0

	if err != nil {
		return fmt.Errorf("flush %d frames: %w", frames, err)
	}
	return nil
}
