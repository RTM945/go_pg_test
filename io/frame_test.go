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

func TestReadFramesSingle(t *testing.T) {
	frames, err := ReadFrames(bytes.NewReader(encodeFrame(123, []byte{0x08, 0x2a})))
	if err != nil {
		t.Fatalf("ReadFrames() error = %v", err)
	}
	if len(frames) != 1 || frames[0].TypeId != 123 || !bytes.Equal(frames[0].Payload, []byte{0x08, 0x2a}) {
		t.Errorf("frames = %+v", frames)
	}
}

// 响应侧和请求侧的区别就在这：多帧是正常的，不是"多余字节"。
// ReadFrame 对同样的字节会报错，ReadFrames 不会。
func TestReadFramesMultiple(t *testing.T) {
	body := append(append(encodeFrame(123, []byte{1}), encodeFrame(124, []byte{2, 3})...),
		encodeFrame(125, nil)...)

	frames, err := ReadFrames(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ReadFrames() error = %v", err)
	}
	want := []struct {
		typeId  int32
		payload []byte
	}{{123, []byte{1}}, {124, []byte{2, 3}}, {125, nil}}

	if len(frames) != len(want) {
		t.Fatalf("读到 %d 帧，want %d", len(frames), len(want))
	}
	for i, w := range want {
		if frames[i].TypeId != w.typeId || !bytes.Equal(frames[i].Payload, w.payload) {
			t.Errorf("frames[%d] = {%d, %x}, want {%d, %x}", i, frames[i].TypeId, frames[i].Payload, w.typeId, w.payload)
		}
	}

	// 同样的字节喂给严格单帧的 ReadFrame 应该报"多余字节"
	if _, _, err := ReadFrame(bytes.NewReader(body)); err == nil {
		t.Error("ReadFrame 对多帧应该报错")
	}
}

func TestReadFramesEmptyStream(t *testing.T) {
	frames, err := ReadFrames(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("空流不该报错: %v", err)
	}
	if len(frames) != 0 {
		t.Errorf("空流读到 %d 帧", len(frames))
	}
}

// 恰好在帧边界结束 = 正常收尾；读到半个包才是坏的。这两者靠 EOF 和
// ErrUnexpectedEOF 区分，错了就会把网络截断当成"正常结束"。
func TestReadFramesTruncatedIsError(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"半个包头", encodeFrame(123, []byte{1, 2, 3})[:5]},
		{"半个包体", encodeFrame(123, []byte{1, 2, 3})[:FrameHeaderSize+1]},
		{"第二帧只写了一半", append(encodeFrame(123, []byte{1}), encodeFrame(124, []byte{2, 3})[:6]...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames, err := ReadFrames(bytes.NewReader(tt.body))
			if err == nil {
				t.Fatalf("被截断了却当成正常结束，读到 %d 帧", len(frames))
			}
			if !errors.Is(err, ErrMalformedFrame) {
				t.Errorf("error = %v, want ErrMalformedFrame", err)
			}
		})
	}
}

func TestReadFramesTooMany(t *testing.T) {
	var body []byte
	for range MaxFrames + 1 {
		body = append(body, encodeFrame(123, []byte{1})...)
	}
	if _, err := ReadFrames(bytes.NewReader(body)); err == nil {
		t.Fatal("超过帧数上限却没过")
	}
}

// AppendFrame 和 WriteFrame 必须产出一模一样的字节——包头布局只有一处定义
func TestAppendFrameMatchesWriteFrame(t *testing.T) {
	payload := []byte{0x08, 0x2a}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, 123, payload); err != nil {
		t.Fatal(err)
	}

	got, err := AppendFrame(nil, 123, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, buf.Bytes()) {
		t.Errorf("AppendFrame = %x, WriteFrame = %x", got, buf.Bytes())
	}

	// 追加语义：拼多帧就是连着 Append
	both, err := AppendFrame(got, 124, []byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte{}, buf.Bytes()...), encodeFrame(124, []byte{9})...); !bytes.Equal(both, want) {
		t.Errorf("连续 Append = %x, want %x", both, want)
	}
}
