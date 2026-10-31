package dispatch

import (
	"context"
	"fmt"
)

// Kind 是告诉客户端的错误类别。业务层只说是哪一类，怎么表达（HTTP 状态码）由传输层决定。
type Kind int

const (
	BadRequest      Kind = iota + 1 // 请求本身不对，客户端改了才能成功
	Unauthorized                    // 凭证无效或过期，客户端回到登录
	Forbidden                       // 凭证有效但不允许（封号）
	NotFound                        // 要操作的对象不存在
	Conflict                        // 和当前状态冲突：重复请求、上一个请求还没完、已经领过
	UpgradeRequired                 // 客户端版本太旧
	Unavailable                     // 服务端暂时不可用，客户端稍后重试
)

// Valid 报告 k 是不是上面定义的类别。
func (k Kind) Valid() bool { return k >= BadRequest && k <= Unavailable }

// ClientError 是要告诉客户端的错误：Code 是协议的一部分（HTTP 下放在 X-Error-Code），
// 客户端靠它决定下一步。
//
// 没有包成 ClientError 的错误，传输层一律当服务端内部错误。所以业务错误在定义的地方
// 就声明好对客户端的契约，不会因为传输层漏了一条映射就变成 500。
type ClientError struct {
	Kind Kind
	Code string
	msg  string
}

// NewClientError 用在包级变量上：类别或错误码不合法时启动就 panic。
func NewClientError(kind Kind, code, msg string) *ClientError {
	if !kind.Valid() || code == "" {
		panic(fmt.Sprintf("dispatch: invalid client error %q (kind %d)", code, kind))
	}
	return &ClientError{Kind: kind, Code: code, msg: msg}
}

func (e *ClientError) Error() string { return e.msg }

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = NewClientError(BadRequest, "UNKNOWN_TYPE", "unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf，或者字段不合法。
	ErrMalformedPayload = NewClientError(BadRequest, "BAD_PAYLOAD", "malformed protocol payload")
)

// DispatchFunc 一次协议分发：typeId、原始 payload、session 都已就绪。S 是 app 自己的 session 类型。
type DispatchFunc[S any] func(ctx context.Context, typeId int32, buffer []byte, session S) error

// ProcessorRegistry 只做 typeId -> handler 的登记和分发。
// 事务、锁、认证、token 都由业务层在注册 handler 时自己包。
type ProcessorRegistry[S any] struct {
	registry map[int32]DispatchFunc[S]
}

func NewProcessorRegistry[S any]() *ProcessorRegistry[S] {
	return &ProcessorRegistry[S]{registry: make(map[int32]DispatchFunc[S])}
}

// Register 登记一个协议号。同一个协议号登记两次直接 panic：后登记的会悄悄覆盖先登记的，
// 谁生效取决于 init 的执行顺序。
func (p *ProcessorRegistry[S]) Register(typeId int32, fn DispatchFunc[S]) {
	if _, ok := p.registry[typeId]; ok {
		panic(fmt.Sprintf("dispatch: typeId %d registered twice", typeId))
	}
	p.registry[typeId] = fn
}

func (p *ProcessorRegistry[S]) Dispatch(ctx context.Context, typeId int32, buffer []byte, session S) error {
	fn, ok := p.registry[typeId]
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownTypeId, typeId)
	}
	return fn(ctx, typeId, buffer, session)
}
