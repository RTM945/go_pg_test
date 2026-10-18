package dispatch

import (
	"context"
	"errors"
	"log/slog"
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

type fakeSession struct {
	uid int64
}

func (s *fakeSession) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }
func (s *fakeSession) UID() int64           { return s.uid }
func (s *fakeSession) SetUID(uid int64)     { s.uid = uid }
func (s *fakeSession) Write(io.Msg) error   { return nil }
func (s *fakeSession) PendingFrames() int   { return 0 }
func (s *fakeSession) Flush() error         { return nil }

func TestRegisterAndDispatch(t *testing.T) {
	reg := NewProcessorRegistry()
	const typeId int32 = 101
	called := false

	reg.Register(typeId, func(_ context.Context, gotTypeId int32, payload []byte, session io.Session) error {
		called = true
		if gotTypeId != typeId {
			t.Errorf("typeId = %d, want %d", gotTypeId, typeId)
		}
		if session.UID() != 7 {
			t.Errorf("uid = %d, want 7", session.UID())
		}
		msg := &testMsg{typeId: typeId}
		if err := msg.Read(payload); err != nil {
			return err
		}
		msg.SetSession(session)
		return nil
	})

	if err := reg.Dispatch(t.Context(), typeId, []byte{0x08, 0x2a}, &fakeSession{uid: 7}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !called {
		t.Fatal("handler 没有被调用")
	}
}

func TestDispatchUnknownTypeId(t *testing.T) {
	reg := NewProcessorRegistry()
	err := reg.Dispatch(t.Context(), 999, nil, &fakeSession{})
	if !errors.Is(err, ErrUnknownTypeId) {
		t.Errorf("Dispatch() error = %v, want ErrUnknownTypeId", err)
	}
}
