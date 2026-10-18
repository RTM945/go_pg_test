package dispatch

import (
	"context"
	"errors"
	"fmt"

	"go_pg_test/io"
)

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = errors.New("unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf。
	ErrMalformedPayload = errors.New("malformed protocol payload")
)

// DispatchFunc 一次协议分发：typeId、原始 payload、Session 都已就绪。
type DispatchFunc func(ctx context.Context, typeId int32, buffer []byte, session io.Session) error

// ProcessorRegistry 只做 typeId -> handler 的登记和分发。
// 事务、锁、认证、token 都由业务层在注册 handler 时自己包。
type ProcessorRegistry struct {
	registry map[int32]DispatchFunc
}

func NewProcessorRegistry() *ProcessorRegistry {
	return &ProcessorRegistry{registry: make(map[int32]DispatchFunc)}
}

func (p *ProcessorRegistry) Register(typeId int32, fn DispatchFunc) {
	p.registry[typeId] = fn
}

func (p *ProcessorRegistry) Dispatch(ctx context.Context, typeId int32, buffer []byte, session io.Session) error {
	fn, ok := p.registry[typeId]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownTypeId, typeId)
	}
	return fn(ctx, typeId, buffer, session)
}
