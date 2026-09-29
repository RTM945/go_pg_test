package main

import (
	"context"
	"fmt"
	"go_pg_test/io"
	"go_pg_test/logic/redenvelope"
	"go_pg_test/protocol"
	"hash/maphash"
	"net/http"
	"pdbgen/dbpool"
	"pdbgen/readxml"
)

func main() {
	schema, err := readxml.LoadSchema("./pdb.xml")
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	err = dbpool.Init(ctx, schema)
	if err != nil {
		panic(err)
	}
	//var uid int64 = 1001
	//lockKey := "user"
	//var processUserRedEnvelopeOnline = func(ctx context.Context) error {
	//	userRedEnvelope := redenvelope.Get(ctx, uid, 1, false)
	//	userRedEnvelope.Online()
	//	return nil
	//}
	//err = dbpool.WithTryAdvisoryLock(ctx, lockKey, uid, processUserRedEnvelopeOnline)
	//if err != nil {
	//	panic(err)
	//}

	reg := NewProcessorRegistry()

	processCRedEnvelope := func(ctx context.Context, req *protocol.CRedEnvelope) error {
		uid := req.Session().UID
		userRedEnvelope := redenvelope.Get(ctx, uid, 1, false)
		userRedEnvelope.Online()
		return nil
	}

	// todo Register 分user调用协议和GM/RPC调用协议
	reg.Register(protocol.CRedEnvelopeTypeId, protocol.NewCRedEnvelope, processCRedEnvelope, processUserTryLock)

	mux := http.NewServeMux()

	mux.HandleFunc("/", handler(reg))

	if err := http.ListenAndServe(":8080", mux); err != nil {
		panic(err)
	}
}

func handler(registry *ProcessorRegistry) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// 用浏览器测试会有这种干扰 先干掉 后面改用Gin
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// todo header 拿出token 去redis 拿到uid
		session := &io.Session{
			UID: 1003,
		}
		//todo 再从body解出协议体
		typeId := protocol.CRedEnvelopeTypeId
		buffer := make([]byte, 1024)
		err := registry.Dispatch(typeId, buffer, session)
		if err != nil {
			// todo 报错
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}
}

func WithUserTryLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.WithTryAdvisoryLock(ctx, uid, fn)
}

func WithUserLock(ctx context.Context, uid int64, fn func(context.Context) error) error {
	return dbpool.WithAdvisoryLock(ctx, uid, fn)
}

var seed = maphash.MakeSeed()

func hashLockNS(name string) int32 {
	return int32(maphash.String(seed, name))
}
func WithNamedLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.WithAdvisoryLock2(ctx, hashLockNS(lockKey), lockValue, fn)
}

func WithNamedTryLock(ctx context.Context, lockKey string, lockValue int32, fn func(context.Context) error) error {
	return dbpool.WithTryAdvisoryLock2(ctx, hashLockNS(lockKey), lockValue, fn)
}

type processFunc[T io.Msg] func(ctx context.Context, req T) error

type dispatchFunc func(buffer []byte, session *io.Session) error

type processMode int

const (
	processQuery processMode = iota
	processUserTryLock
	processUserLock
	processNamedTryLock
	processNamedLock
)

type ProcessorRegistry struct {
	registry map[int32]dispatchFunc
}

func NewProcessorRegistry() *ProcessorRegistry {
	return &ProcessorRegistry{registry: make(map[int32]dispatchFunc)}
}

func (p *ProcessorRegistry) Register[T io.Msg](typeId int32, newMsg func() T, fn processFunc[T], mode processMode) {
	p.registry[typeId] = func(buffer []byte, session *io.Session) error {
		msg := newMsg()
		if err := msg.Read(buffer); err != nil {
			return fmt.Errorf("decode typeId %d failed: %w", typeId, err)
		}
		msg.SetSession(session)

		process := func(ctx context.Context) error {
			return fn(ctx, msg)
		}

		// db!
		ctx := context.Background()
		switch mode {
		case processQuery:
			return dbpool.Query(ctx, process)
		case processUserTryLock:
			return WithUserTryLock(ctx, session.UID, process)
		case processUserLock:
			return WithUserLock(ctx, session.UID, process)
		default:
			// todo processNamedTryLock 和 processNamedLock 基本用不到
			return fmt.Errorf("unknown process mode %d for typeId %d", mode, typeId)
		}
	}
}

func (p *ProcessorRegistry) Dispatch(typeId int32, buffer []byte, session *io.Session) error {
	fn, ok := p.registry[typeId]
	if !ok {
		return fmt.Errorf("no processor registered for typeId %d", typeId)
	}
	return fn(buffer, session)
}
