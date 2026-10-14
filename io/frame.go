package io

import (
	"encoding/binary"
	"errors"
	"fmt"
	stdio "io"
)

const (
	// FrameHeaderSize 包头固定长度：4 字节 payload 长度 + 4 字节 typeId。
	FrameHeaderSize = 8
	// MaxPayloadSize payload 长度上限，防止对端填一个超大 length 把服务端撑爆。
	MaxPayloadSize = 1 << 20
)

// ErrMalformedFrame 对端发来的字节流不符合封包格式，属于客户端错误。
var ErrMalformedFrame = errors.New("malformed frame")

// ReadFrame 从 r 读出一帧，返回协议号 typeId 和它的 payload。
//
// 封包格式（大端）：
//
//	+--------+--------+---------------+
//	| length | typeId |    payload    |
//	|  4B    |  4B    |  length 字节  |
//	+--------+--------+---------------+
//
// length 是 payload 的字节数，不含 8 字节包头。
// 只收单帧：读完一帧后 r 必须恰好结束，多出来的字节说明客户端和服务端对
// 封包格式的理解不一致，直接报错而不是静默丢弃。
func ReadFrame(r stdio.Reader) (typeId int32, payload []byte, err error) {
	var header [FrameHeaderSize]byte
	if _, err := stdio.ReadFull(r, header[:]); err != nil {
		return 0, nil, fmt.Errorf("%w: read header: %w", ErrMalformedFrame, err)
	}

	length := binary.BigEndian.Uint32(header[:4])
	typeId = int32(binary.BigEndian.Uint32(header[4:8]))
	if length > MaxPayloadSize {
		return 0, nil, fmt.Errorf("%w: payload length %d over limit %d", ErrMalformedFrame, length, MaxPayloadSize)
	}

	payload = make([]byte, length)
	if _, err := stdio.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("%w: read payload: %w", ErrMalformedFrame, err)
	}

	var extra [1]byte
	if n, _ := r.Read(extra[:]); n > 0 {
		return 0, nil, fmt.Errorf("%w: trailing bytes after frame", ErrMalformedFrame)
	}
	return typeId, payload, nil
}

// WriteFrame 把 payload 按封包格式写成一帧，和 ReadFrame 对称。
//
// 封包格式就这一处定义：服务端回包、调试客户端发包、实机测试发包都走这里，
// 免得格式改一处漏两处。
func WriteFrame(w stdio.Writer, typeId int32, payload []byte) error {
	// 发出去的包超过上限，对端也会拒，不如在本地就拦住
	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("typeId %d: payload size %d exceeds limit %d", typeId, len(payload), MaxPayloadSize)
	}

	var header [FrameHeaderSize]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(header[4:8], uint32(typeId))

	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("typeId %d: write header: %w", typeId, err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("typeId %d: write payload: %w", typeId, err)
	}
	return nil
}
