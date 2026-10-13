package io

type Msg interface {
	SetSession(session *Session)
	Session() *Session
	TypeId() int32
	// Size 是 Write 需要的缓冲区大小，编码前就得知道。
	Size() int
	Read(buffer []byte) error
	Write(buffer []byte) (int, error)
}
