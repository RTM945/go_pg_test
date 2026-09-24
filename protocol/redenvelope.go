package protocol

import "go_pg_test/io"

type CRedEnvelope struct {
	session *io.Session
	typeId  int32

	ActId int32
}

const CRedEnvelopeTypeId int32 = 123

func NewCRedEnvelope() *CRedEnvelope {
	return &CRedEnvelope{
		typeId: CRedEnvelopeTypeId,
	}
}

func (m *CRedEnvelope) SetSession(session *io.Session) {
	m.session = session
}

func (m *CRedEnvelope) Session() *io.Session {
	return m.session
}

func (m *CRedEnvelope) TypeId() int32 {
	return m.typeId
}

func (m *CRedEnvelope) Read(buffer []byte) error {
	// todo 解码
	m.ActId = 1
	return nil
}
