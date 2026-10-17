package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"go_pg_test/io"
)

type testMsg struct {
	session io.Session
	typeId  int32
	payload []byte
}

func (m *testMsg) SetSession(s io.Session)     { m.session = s }
func (m *testMsg) Session() io.Session         { return m.session }
func (m *testMsg) TypeId() int32               { return m.typeId }
func (m *testMsg) Size() int                   { return len(m.payload) }
func (m *testMsg) Read(b []byte) error         { m.payload = append(m.payload[:0], b...); return nil }
func (m *testMsg) Write(b []byte) (int, error) { return copy(b, m.payload), nil }

func TestRegisterAndDispatch(t *testing.T) {
	reg := NewProcessorRegistry()
	const typeId int32 = 101
	called := false

	reg.Register(typeId, func(_ context.Context, gotTypeId int32, payload []byte, s io.Session) error {
		called = true
		if gotTypeId != typeId {
			t.Errorf("typeId = %d, want %d", gotTypeId, typeId)
		}
		if s.UID() != 7 {
			t.Errorf("uid = %d, want 7", s.UID())
		}
		msg := &testMsg{typeId: typeId}
		if err := msg.Read(payload); err != nil {
			return err
		}
		msg.SetSession(s)
		return nil
	})

	s := NewSession(httptest.NewRecorder(), nil)
	s.SetUID(7)
	if err := reg.Dispatch(t.Context(), typeId, []byte{0x08, 0x2a}, s); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !called {
		t.Fatal("handler 没有被调用")
	}
}

func TestDispatchUnknownTypeId(t *testing.T) {
	reg := NewProcessorRegistry()
	err := reg.Dispatch(t.Context(), 999, nil, NewSession(httptest.NewRecorder(), nil))
	if !errors.Is(err, ErrUnknownTypeId) {
		t.Errorf("Dispatch() error = %v, want ErrUnknownTypeId", err)
	}
}
