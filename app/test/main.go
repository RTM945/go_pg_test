package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/maphash"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go_pg_test/io"
	"go_pg_test/logging"
	"pdbgen/dbpool"
	"pdbgen/readxml"
)

// shutdownTimeout 优雅关闭的等待上限，超过就强断。
const shutdownTimeout = 10 * time.Second

var (
	// ErrUnknownTypeId 对端发来了没有注册处理函数的协议号。
	ErrUnknownTypeId = errors.New("unknown protocol type id")
	// ErrMalformedPayload 协议体不是该协议号对应的合法 protobuf。
	ErrMalformedPayload = errors.New("malformed protocol payload")
)

var (
	schemaPath = flag.String("schema", "app/test/pdb.xml", "path to pdb.xml")
	addr       = flag.String("addr", ":8080", "listen address")
	logLevel   = flag.String("log-level", "info", "log level: debug / info / warn / error")
	logFormat  = flag.String("log-format", "text", "log format: text / json")
	logPath    = flag.String("log-path", "", "log file path; empty writes to stdout")
	logMaxSize = flag.Int("log-max-size", 100, "rotate the log file after this many MB")
	logBackups = flag.Int("log-backups", 7, "how many rotated log files to keep")
)

// newLogger 建运维日志的 logger。
//
// 刻意不调 slog.SetDefault：这个项目会有多个 logger（运维日志一个、业务埋点
// 一个，落点、格式、保留策略都不一样），一旦设了全局默认，代码里随手写的
// slog.Info 就都钉死在那一个上了。logger 一律显式持有、显式往下传。
//
// 要加 BI 日志就在这儿再建一个：换一份 Options（另一个 Path、可能另一套滚动
// 策略），显式递给需要它的地方。
func newLogger() (*slog.Logger, func() error, error) {
	// LevelVar 而不是固定值：以后要支持运行期改级别，改这个变量就行
	var lv slog.LevelVar
	if err := lv.UnmarshalText([]byte(*logLevel)); err != nil {
		return nil, nil, fmt.Errorf("bad -log-level %q: want debug / info / warn / error", *logLevel)
	}

	return logging.NewLogger(logging.Options{
		Path:       *logPath,
		Format:     *logFormat,
		MaxSizeMB:  *logMaxSize,
		MaxBackups: *logBackups,
		MaxAgeDays: 30,
		Compress:   true,
	}, &lv)
}

var processRegistry = NewProcessorRegistry()

func main() {
	flag.Parse()

	// 运维日志。将来要加业务埋点的话，在这里再 new 一个落到别处的 logger，
	// 显式传给需要它的地方——别去 slog.SetDefault。
	log, closeLog, err := newLogger()
	if err != nil {
		fmt.Fprintln(os.Stderr, "main:", err)
		os.Exit(2)
	}

	// 把 SQL 日志接过来。dbpool 内部不设全局，必须显式注入。
	dbpool.SetLogger(log)

	schema, err := readxml.LoadSchema(*schemaPath)
	if err != nil {
		log.Error("LoadSchema", "path", *schemaPath, "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	if err := dbpool.Init(ctx, schema); err != nil {
		log.Error("dbpool.Init", "err", err)
		os.Exit(1)
	}

	// 收到 Ctrl+C / SIGTERM 就取消
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("listening", "app", schema.AppName, "addr", *addr, "schema", *schemaPath, "showSQL", schema.ShowSQL)
	serveErr := serve(ctx, &http.Server{Addr: *addr, Handler: newMux(processRegistry, log)}, shutdownTimeout, log)

	// 收摊必须排在 serve 之后，不能在它前面——理由见 dbpool.Close 的注释。
	// 这里都走显式调用 + 单出口：os.Exit 不会跑 defer，而日志文件必须比
	// 最后一条日志活得久，所以 closeLog 排在所有 log 调用之后。
	dbpool.Close()

	code := 0
	if serveErr != nil {
		log.Error("serve", "err", serveErr)
		code = 1
	} else {
		log.Info("bye")
	}

	_ = closeLog()
	os.Exit(code)
}

// serve 起服务，ctx 一取消就优雅关闭：不再收新连接，等在途请求跑完。
//
// ListenAndServe 是阻塞的，所以它得在 goroutine 里跑，主流程留着等 ctx。
// Shutdown 的语义是"等"，它不会去取消在途请求的 ctx——所以这里的超时
// 不是用来打断请求的，是最后兜底：等不到就强关，并让这件事在日志里可见。
func serve(ctx context.Context, srv *http.Server, timeout time.Duration, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		// 还没等到信号就退出了，多半是端口被占
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	case <-ctx.Done():
		log.Info("Receive shutdown signal, wait for shutdown to complete")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("Shutdown timeout, force close", "timeout", timeout, "err", err)
		_ = srv.Close()
	}
	return nil
}

// newMux 单独拆出来是为了能直接测路由：只有 POST 会进 handler，
// 浏览器拿 GET 去摸 /favicon.ico 在 mux 这一层就被挡掉，
// 不用在 handler 里自己判 r.URL.Path。
func newMux(reg *ProcessorRegistry, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", handler(reg, log))
	return mux
}

func handler(reg *ProcessorRegistry, log *slog.Logger) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// body 就是一帧二进制，解出协议号和协议体
		typeId, payload, err := io.ReadFrame(r.Body)
		if err != nil {
			// 帧都读不出来，一定是客户端的问题。
			// 这里必须留痕：静默拒掉的话，日志里完全看不出"有请求来过"，
			// 排查时就只能靠猜（客户端协议对不上是开发期最常见的情况）。
			log.Warn("Decode frame failed", "remote", r.RemoteAddr, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// todo header 拿出token 去redis 拿到uid
		session := &io.Session{
			UID: 1003,
			W:   w,
			Log: log,
		}

		if err := reg.Dispatch(typeId, payload, session); err != nil {
			// 协议号不认识、协议体解不出，都是客户端发错了
			if errors.Is(err, ErrUnknownTypeId) || errors.Is(err, ErrMalformedPayload) {
				log.Warn("Wrong typeId", "typeId", typeId, "remote", r.RemoteAddr, "err", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// 同一个 uid 的上一次请求还占着咨询锁。这不是服务端的错，
			// 语义是"你上一次还没做完，稍后重试"——回 500 的话客户端会当成 bug。
			if errors.Is(err, dbpool.ErrAdvisoryLockNotAcquired) {
				log.Info("previous request for this user is still in flight", "typeId", typeId, "uid", session.UID)
				http.Error(w, "previous request for this user is still in flight", http.StatusConflict)
				return
			}
			// 剩下的才是服务端自己的问题，不把内部细节漏给客户端，但要留日志
			log.Error("dispatch failed", "typeId", typeId, "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
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
