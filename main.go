package main

import (
	"context"
	"fmt"
	"go_pg_test/io"
	"go_pg_test/logic/redenvelope"
	"go_pg_test/protocol"
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

	defaultUserLock := func(session *io.Session) (string, int64) {
		return "user", session.UID
	}

	// todo processTryLock defaultUserLock 可以放代码生成
	reg.Register(protocol.CRedEnvelopeTypeId, protocol.NewCRedEnvelope, processCRedEnvelope, processTryLock, defaultUserLock)

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

type processFunc[T io.Msg] func(ctx context.Context, req T) error

type dispatchFunc func(buffer []byte, session *io.Session) error

type processMode int

const (
	processQuery processMode = iota
	processTryLock
	processLock
)

type lockKeyFunc func(session *io.Session) (string, int64)

type ProcessorRegistry struct {
	registry map[int32]dispatchFunc
}

func NewProcessorRegistry() *ProcessorRegistry {
	return &ProcessorRegistry{registry: make(map[int32]dispatchFunc)}
}

func (p *ProcessorRegistry) Register[T io.Msg](typeId int32, newMsg func() T, fn processFunc[T], mode processMode, lockKey lockKeyFunc) {
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

		case processTryLock:
			keyName, keyValue := lockKey(session)

			return dbpool.WithTryAdvisoryLock(ctx, keyName, keyValue, process)

		case processLock:
			keyName, keyValue := lockKey(session)

			return dbpool.WithAdvisoryLock(ctx, keyName, keyValue, process)

		default:
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
