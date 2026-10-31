package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeSession 注册表不关心 session 是什么类型，原样交给处理函数。
type fakeSession struct {
	uid int64
}

func TestRegisterAndDispatch(t *testing.T) {
	reg := NewProcessorRegistry[*fakeSession]()
	const typeId int32 = 101
	called := false

	reg.Register(typeId, func(_ context.Context, gotTypeId int32, payload []byte, session *fakeSession) error {
		called = true
		if gotTypeId != typeId {
			t.Errorf("typeId = %d, want %d", gotTypeId, typeId)
		}
		if session.uid != 7 {
			t.Errorf("uid = %d, want 7", session.uid)
		}
		if !bytes.Equal(payload, []byte{0x08, 0x2a}) {
			t.Errorf("payload = %x, want 082a", payload)
		}
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
	reg := NewProcessorRegistry[*fakeSession]()
	err := reg.Dispatch(t.Context(), 999, nil, &fakeSession{})
	if !errors.Is(err, ErrUnknownTypeId) {
		t.Errorf("Dispatch() error = %v, want ErrUnknownTypeId", err)
	}
}

// TestRegisterTwicePanics 同一个协议号登记两次直接 panic，不让后登记的悄悄覆盖。
func TestRegisterTwicePanics(t *testing.T) {
	reg := NewProcessorRegistry[*fakeSession]()
	noop := func(context.Context, int32, []byte, *fakeSession) error { return nil }
	reg.Register(101, noop)

	defer func() {
		if recover() == nil {
			t.Fatal("重复登记应该 panic")
		}
	}()
	reg.Register(101, noop)
}

// TestClientErrorContract ClientError 包在别的错误里也能取出来；类别不合法启动就 panic。
func TestClientErrorContract(t *testing.T) {
	err := fmt.Errorf("%w: typeId 7", ErrMalformedPayload)
	ce, ok := errors.AsType[*ClientError](err)
	if !ok || ce.Kind != BadRequest || ce.Code != "BAD_PAYLOAD" {
		t.Fatalf("应该取出 BAD_PAYLOAD，实际 %v %v", ce, ok)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("非法类别应该 panic")
		}
	}()
	NewClientError(Kind(0), "X", "x")
}
