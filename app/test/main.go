package main

import (
	"context"
	"flag"
	"fmt"
	"go_pg_test/common/logging"
	"go_pg_test/common/redis"
	"go_pg_test/common/server"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pdbgen/dbpool"
	"pdbgen/readxml"
)

// shutdownTimeout 优雅关闭的等待上限，超过就强断。
const shutdownTimeout = 10 * time.Second

var (
	schemaPath = flag.String("schema", "app/test/pdb.xml", "path to pdb.xml")
	addr       = flag.String("addr", ":8080", "listen address")
	logLevel   = flag.String("log-level", "info", "log level: debug / info / warn / error")
	logFormat  = flag.String("log-format", "text", "log format: text / json")
	logPath    = flag.String("log-path", "", "log file path; empty writes to stdout")
	logMaxSize = flag.Int("log-max-size", 100, "rotate the log file after this many MB")
	logBackups = flag.Int("log-backups", 7, "how many rotated log files to keep")
)

// 要加 BI 日志就在这儿再建一个：换一份 Options（另一个 Path、可能另一套滚动策略），显式递给需要它的地方。
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

// newMux 单独拆出来是为了能直接测路由：只有 POST 会进 handler，
// 浏览器拿 GET 去摸 /favicon.ico 在 mux 这一层就被挡掉，
// 不用在 handler 里自己判 r.URL.Path。
func newMux(log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", handler(log))
	return mux
}

func main() {
	flag.Parse()

	// 运维日志。将来要加业务埋点的话，在这里再 new 一个落到别处的 logger，
	// 显式传给需要它的地方——别去 slog.SetDefault。
	log, closeLog, err := newLogger()
	if err != nil {
		fmt.Fprintln(os.Stderr, "main:", err)
		os.Exit(2)
	}
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

	// sql日志
	dbpool.SetLogger(log)

	// redis
	err = redis.Init("127.0.0.1:6379", "123456")
	if err != nil {
		fmt.Fprintln(os.Stderr, "main:", err)
		os.Exit(2)
	}

	// 收到 Ctrl+C / SIGTERM 就取消
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("listening", "app", schema.AppName, "addr", *addr, "schema", *schemaPath, "showSQL", schema.ShowSQL)
	// 阻塞
	serveErr := server.Serve(ctx, &http.Server{Addr: *addr, Handler: newMux(log)}, shutdownTimeout, log)

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
