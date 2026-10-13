package io

import (
	"fmt"
	"net/http"
)

type Session struct {
	UID int64
	W   http.ResponseWriter
}

// Write 把 msg 编好，按封包格式写回客户端。封包格式本身在 WriteFrame 里。
func (session *Session) Write[T Msg](msg T) error {
	payload := make([]byte, msg.Size())
	n, err := msg.Write(payload)
	if err != nil {
		return fmt.Errorf("编码 typeId %d: %w", msg.TypeId(), err)
	}
	if n != len(payload) {
		// Size() 和实际编出来的长度对不上，发出去的帧会是坏的，宁可在这里炸
		return fmt.Errorf("编码 typeId %d: 编出 %d 字节，Size() 报的是 %d", msg.TypeId(), n, len(payload))
	}
	return WriteFrame(session.W, msg.TypeId(), payload)
}
