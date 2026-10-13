package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/maphash"
	"log"
	"net/http"

	"go_pg_test/io"
	"pdbgen/dbpool"
	"pdbgen/readxml"
)

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = errors.New("unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf。
	ErrMalformedPayload = errors.New("malformed protocol payload")
)

var (
	// 默认值按"二进制旁边放配置"的目录形态给，仓库相对路径由 makefile 传，
	// 别把 app/test/ 这种位置信息写进代码里。
	schemaPath = flag.String("schema", "pdb.xml", "pdb.xml 的路径")
	addr       = flag.String("addr", ":8080", "监听地址")
)

func main() {
	flag.Parse()

	schema, err := readxml.LoadSchema(*schemaPath)
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	err = dbpool.Init(ctx, schema)
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", handler(processRegistry))

	log.Printf("appName=%s listening on %s, schema=%s", schema.AppName, *addr, *schemaPath)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		panic(err)
	}
}

func handler(reg *ProcessorRegistry) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// 用浏览器测试会有这种干扰 先干掉 后面改用Gin
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// body 就是一帧二进制，解出协议号和协议体
		typeId, payload, err := io.ReadFrame(r.Body)
		if err != nil {
			// 帧都读不出来，一定是客户端的问题
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// todo header 拿出token 去redis 拿到uid
		session := &io.Session{
			UID: 1003,
			W:   w,
		}

		if err := reg.Dispatch(typeId, payload, session); err != nil {
			// 协议号不认识、协议体解不出，都是客户端发错了
			if errors.Is(err, ErrUnknownTypeId) || errors.Is(err, ErrMalformedPayload) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// 剩下的才是服务端自己的问题，不把内部细节漏给客户端，但要留日志
			log.Printf("dispatch typeId %d failed: %v", typeId, err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		// 成功这里不用再写状态码：响应帧由处理器通过 session.Write 写出，
		// 那次 Write 会带上 200；处理器没写的话 net/http 也会补一个空 200。
		// 在这里再 WriteHeader 一次是多余的，日志里会报 superfluous。
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
			return fmt.Errorf("%w: typeId %d: %w", ErrMalformedPayload, typeId, err)
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
		return fmt.Errorf("%w: %d", ErrUnknownTypeId, typeId)
	}
	return fn(buffer, session)
}
