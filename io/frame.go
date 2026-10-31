package io

import (
	"encoding/binary"
	"errors"
	"fmt"
	stdio "io"
	"slices"
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
	typeId, payload, err = readOneFrame(r)
	if err != nil {
		return 0, nil, err
	}

	var extra [1]byte
	if n, _ := r.Read(extra[:]); n > 0 {
		return 0, nil, fmt.Errorf("%w: trailing bytes after frame", ErrMalformedFrame)
	}
	return typeId, payload, nil
}

// readOneFrame 读一帧，不管后面还有没有别的字节。
// 严格性（读完必须结束）由 ReadFrame 加，多帧由 ReadFrames 循环——两者共用这一份解析。
func readOneFrame(r stdio.Reader) (typeId int32, payload []byte, err error) {
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
	return typeId, payload, nil
}

// AppendFrame 把一帧追加到 dst 后面。
//
// 用它攒多帧再一次写出，比赛多次 WriteFrame 省 syscall。要发的是消息时用 AppendMsg，
// 省掉中间那份 payload。
func AppendFrame(dst []byte, typeId int32, payload []byte) ([]byte, error) {
	if err := checkPayloadSize(typeId, len(payload)); err != nil {
		return nil, err
	}
	dst = appendHeader(dst, typeId, len(payload))
	return append(dst, payload...), nil
}

// AppendMsg 把 m 编成一帧追加到 dst 后面：只算一次大小，直接编进 dst，
// 不先编进一份单独的 payload 再复制。服务端回包用它。
func AppendMsg(dst []byte, m Msg) ([]byte, error) {
	typeId, size := m.TypeId(), m.SizeVT()
	if err := checkPayloadSize(typeId, size); err != nil {
		return nil, err
	}
	dst = appendHeader(dst, typeId, size)
	off := len(dst)
	dst = slices.Grow(dst, size)[:off+size]
	n, err := m.MarshalToSizedBufferVT(dst[off:])
	if err != nil {
		return nil, fmt.Errorf("typeId %d: encode: %w", typeId, err)
	}
	if n != size {
		// SizeVT 和实际编出来的长度对不上，发出去的帧会是坏的，宁可不发
		return nil, fmt.Errorf("typeId %d: encoded %d bytes but SizeVT() says %d", typeId, n, size)
	}
	return dst, nil
}

// appendHeader 追加包头。包头怎么排只有这一处知道。
func appendHeader(dst []byte, typeId int32, payloadLen int) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(payloadLen))
	return binary.BigEndian.AppendUint32(dst, uint32(typeId))
}

// checkPayloadSize 发出去的包超过上限，对端也会拒，不如在本地就拦住。
func checkPayloadSize(typeId int32, n int) error {
	if n > MaxPayloadSize {
		return fmt.Errorf("typeId %d: payload size %d exceeds limit %d", typeId, n, MaxPayloadSize)
	}
	return nil
}

// WriteFrame 把 payload 按封包格式写成一帧，和 ReadFrame 对称。
func WriteFrame(w stdio.Writer, typeId int32, payload []byte) error {
	frame, err := AppendFrame(nil, typeId, payload)
	if err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("typeId %d: write: %w", typeId, err)
	}
	return nil
}

// Frame 一帧解出来的结果。
type Frame struct {
	TypeId  int32
	Payload []byte
}

// MaxFrames 一次响应最多几帧。防对端发无穷帧把内存撑爆。
const MaxFrames = 64

// ReadFrames 连续读到流结束，返回期间读到的所有帧。
//
// 响应侧用它：一次请求可能触发好几个模块，各自的响应协议会一起发回来，
// 通常 1 帧，也可能几帧。请求侧仍然用 ReadFrame（严格单帧），那边的
// "多余字节就报错"是防客户端格式写错的，有价值。
//
// 边界靠 ReadFrame 的语义分：
//   - 恰好在帧边界读到流尾 → 正常结束，返回已读到的帧
//   - 读到半个包头或半个包体 → ReadFrame 会包成 ErrUnexpectedEOF，是真坏了
func ReadFrames(r stdio.Reader) ([]Frame, error) {
	frames := make([]Frame, 0, 4)

	for {
		typeId, payload, err := readOneFrame(r)
		if errors.Is(err, stdio.EOF) {
			return frames, nil
		}
		if err != nil {
			return nil, err
		}
		if len(frames) >= MaxFrames {
			return nil, fmt.Errorf("frame count exceeds limit %d", MaxFrames)
		}
		frames = append(frames, Frame{TypeId: typeId, Payload: payload})
	}
}
