package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options 一个日志落点的配置。ops 和 BI 各给一份就是两个不同的落点。
type Options struct {
	// Path 为空表示写 stdout——本地开发不用先建目录。
	Path string

	// Format 只认 text（默认）和 json。
	//
	// text 走本包的自定义 handler：[]string 属性按行展开，SQL batch 才读得下去。
	// json 直接用 slog 的 JSONHandler——数组天然就是数组，不需要展开。
	Format string

	// MaxSizeMB 单个文件多大开始滚动。
	MaxSizeMB int
	// MaxBackups 最多留几个历史文件，0 表示不限。
	MaxBackups int
	// MaxAgeDays 历史文件最多留几天，0 表示不限。
	MaxAgeDays int
	// Compress 历史文件要不要 gzip。
	Compress bool
}

// NewLogger 按配置建一个 logger：滚动 writer + 按格式选 handler。
//
// 第二个返回值是关闭函数，进程退出前要调——它释放日志文件句柄。
// 不调的话 Windows 上文件一直被占着，外部删不掉也挪不动。
// 写 stdout 时它是个空操作。
//
// level 传 *slog.LevelVar 的话，运行期改级别立刻生效，不用重启。
func NewLogger(opts Options, level slog.Leveler) (*slog.Logger, func() error, error) {
	switch opts.Format {
	case "", "text", "json":
	default:
		return nil, nil, fmt.Errorf("bad log format %q: want text or json", opts.Format)
	}

	w, err := newWriter(opts)
	if err != nil {
		return nil, nil, err
	}

	closeFn := func() error { return nil }
	if c, ok := w.(io.Closer); ok {
		closeFn = c.Close
	}

	if opts.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})), closeFn, nil
	}
	return slog.New(NewHandler(w, level)), closeFn, nil
}

func newWriter(opts Options) (io.Writer, error) {
	if opts.Path == "" {
		return os.Stdout, nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, err
	}

	return &lumberjack.Logger{
		Filename:   opts.Path,
		MaxSize:    opts.MaxSizeMB,
		MaxBackups: opts.MaxBackups,
		MaxAge:     opts.MaxAgeDays,
		Compress:   opts.Compress,
		LocalTime:  true,
	}, nil
}
