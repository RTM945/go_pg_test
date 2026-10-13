package io

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

// fakeMsg 一个最小的 Msg，用来单独验证 Session.Write 的封包格式。
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

// Write 出来的帧必须能被 ReadFrame 原样读回来——两边是同一套格式，
// 中间错一个字节客户端就解不出来。
func TestSessionWriteRoundTrip(t *testing.T) {
	payload := []byte{0x08, 0x2a}
	rec := httptest.NewRecorder()
	s := &Session{UID: 1003, W: rec}

	if err := s.Write(&fakeMsg{typeId: 123, payload: payload}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// ReadFrame 自己会校验帧后没有多余字节，所以这一条同时验证了长度写对了
	typeId, got, err := ReadFrame(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("ReadFrame() error = %v，写出来的字节是 %x", err, rec.Body.Bytes())
	}
	if typeId != 123 {
		t.Errorf("typeId = %d, want 123", typeId)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestSessionWriteEmptyPayload(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Session{W: rec}

	if err := s.Write(&fakeMsg{typeId: 124}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if want := FrameHeaderSize; rec.Body.Len() != want {
		t.Errorf("写了 %d 字节，want %d（只有包头）", rec.Body.Len(), want)
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
	if rec.Body.Len() != 0 {
		t.Errorf("报错了却已经写出 %d 字节: %x", rec.Body.Len(), rec.Body.Bytes())
	}
}
