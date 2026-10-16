package io

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

// fakeMsg 一个最小的 Msg，用来单测 Session 的攒帧/发送。
type fakeMsg struct {
	session *Session
	typeId  int32
	payload []byte
	size    int // 非 0 时假装 Size() 是这个值，用来测长度对不上的情况
}

func (m *fakeMsg) SetSession(s *Session)       { m.session = s }
func (m *fakeMsg) Session() *Session           { return m.session }
func (m *fakeMsg) TypeId() int32               { return m.typeId }
func (m *fakeMsg) Size() int                   { return max(m.size, len(m.payload)) }
func (m *fakeMsg) Read(b []byte) error         { m.payload = append(m.payload[:0], b...); return nil }
func (m *fakeMsg) Write(b []byte) (int, error) { return copy(b, m.payload), nil }

// Write 只攒不发：调 Flush 之前响应体必须是空的。这是"事务提交成功才发"
// 那条保证的落点——业务跑到一半发出去了，回滚就撤不回来了。
func TestSessionWriteOnlyAccumulates(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{UID: 1003, W: rec}

	if err := s.Write(&fakeMsg{typeId: 123, payload: []byte{0x08, 0x2a}}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if rec.Body.Len() != 0 {
		t.Errorf("Flush 之前就写出去了 %d 字节: %x", rec.Body.Len(), rec.Body.Bytes())
	}
	if got := s.PendingFrames(); got != 1 {
		t.Errorf("PendingFrames() = %d, want 1", got)
	}
}

// 没调 Flush 就什么都不发。业务出错时 handler 直接 return、不走 Flush，
// 客户端收到的是错误状态码，而不是一个已经回滚了的"成功"响应。
func TestSessionNotFlushedWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{W: rec}

	if err := s.Write(&fakeMsg{typeId: 123, payload: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	// 这里模拟 Dispatch 返回错误后 handler 直接 return

	if rec.Body.Len() != 0 {
		t.Errorf("没调 Flush 却写出了 %d 字节: %x", rec.Body.Len(), rec.Body.Bytes())
	}
}

// 攒多帧一次写出，客户端循环读能原样读回来——一次请求触发多个模块时就是这样。
func TestSessionFlushMultipleFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{UID: 1003, W: rec}

	msgs := []*fakeMsg{
		{typeId: 123, payload: []byte{0x08, 0x2a}},
		{typeId: 124, payload: []byte{0x10, 0x01, 0x18, 0x02}},
		{typeId: 125, payload: nil},
	}
	for _, m := range msgs {
		if err := s.Write(m); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if got := s.PendingFrames(); got != len(msgs) {
		t.Fatalf("Flush 前 PendingFrames() = %d, want %d", got, len(msgs))
	}

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	frames, err := ReadFrames(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("ReadFrames() error = %v，字节 %x", err, rec.Body.Bytes())
	}
	if len(frames) != len(msgs) {
		t.Fatalf("读到 %d 帧，want %d", len(frames), len(msgs))
	}
	for i, m := range msgs {
		if frames[i].TypeId != m.typeId || !bytes.Equal(frames[i].Payload, m.payload) {
			t.Errorf("frames[%d] = {%d, %x}, want {%d, %x}",
				i, frames[i].TypeId, frames[i].Payload, m.typeId, m.payload)
		}
	}

	// Flush 之后攒的应该清干净，免得同一个 Session 被复用时有残留
	if got := s.PendingFrames(); got != 0 {
		t.Errorf("Flush 之后 PendingFrames() = %d, want 0", got)
	}
}

func TestSessionFlushNothingIsNoop(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{W: rec}

	if err := s.Flush(); err != nil {
		t.Fatalf("空 Flush() error = %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("空 Flush 写出了 %d 字节", rec.Body.Len())
	}
}

// Size() 和实际编出来的长度对不上时必须拦住：放过去的话写出去的是个坏帧，
// 客户端解不出来还查不到原因。
func TestSessionWriteSizeMismatch(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{W: rec}

	err := s.Write(&fakeMsg{typeId: 123, payload: []byte{1, 2, 3}, size: 10})
	if err == nil {
		t.Fatal("Write() = nil, want error")
	}
	if s.PendingFrames() != 0 {
		t.Errorf("报错了却攒下了 %d 帧", s.PendingFrames())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("报错了却写出了 %d 字节", rec.Body.Len())
	}
}
