package io

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// encodeFrame 按封包格式拼一帧，测试里当客户端用。
func encodeFrame(typeId int32, payload []byte) []byte {
	buf := make([]byte, FrameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(buf[4:8], uint32(typeId))
	copy(buf[FrameHeaderSize:], payload)
	return buf
}

func TestReadFrame(t *testing.T) {
	payload := []byte{0x08, 0x2a}
	typeId, got, err := ReadFrame(bytes.NewReader(encodeFrame(123, payload)))
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if typeId != 123 {
		t.Errorf("typeId = %d, want 123", typeId)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestReadFrameEmptyPayload(t *testing.T) {
	typeId, got, err := ReadFrame(bytes.NewReader(encodeFrame(124, nil)))
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if typeId != 124 {
		t.Errorf("typeId = %d, want 124", typeId)
	}
	if len(got) != 0 {
		t.Errorf("payload = %x, want empty", got)
	}
}

// WriteFrame 编出来的必须和上面那份独立实现一字不差——那份是封包格式的基准，
// 两边都改才过得去的话就说明没锚住了。
func TestWriteFrameMatchesSpec(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0x08, 0x2a}, bytes.Repeat([]byte{0xab}, 300)} {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, 123, payload); err != nil {
			t.Fatalf("WriteFrame(%d 字节) error = %v", len(payload), err)
		}
		if want := encodeFrame(123, payload); !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("payload %d 字节: WriteFrame() = %x, want %x", len(payload), buf.Bytes(), want)
		}
	}
}

func TestWriteFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, 123, make([]byte, MaxPayloadSize+1)); err == nil {
		t.Fatal("WriteFrame() = nil, want error")
	}
	if buf.Len() != 0 {
		t.Errorf("报错了却已经写出 %d 字节", buf.Len())
	}
}

func TestReadFrameMalformed(t *testing.T) {
	overLimit := encodeFrame(123, nil)
	binary.BigEndian.PutUint32(overLimit[:4], MaxPayloadSize+1)

	tests := []struct {
		name string
		body []byte
	}{
		{"body 为空", nil},
		{"包头不足 8 字节", encodeFrame(123, []byte{1})[:5]},
		{"payload 被截断", encodeFrame(123, []byte{1, 2, 3})[:FrameHeaderSize+1]},
		{"length 超过上限", overLimit},
		{"帧后有多余字节", append(encodeFrame(123, []byte{1}), 0xff)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ReadFrame(bytes.NewReader(tt.body))
			if !errors.Is(err, ErrMalformedFrame) {
				t.Fatalf("ReadFrame() error = %v, want ErrMalformedFrame", err)
			}
		})
	}
}
