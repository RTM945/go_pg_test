package io

type Msg interface {
	SetSession(session *Session)
	Session() *Session
	TypeId() int32
	Read(buffer []byte) error
}
