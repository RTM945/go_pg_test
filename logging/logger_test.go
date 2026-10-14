package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ops.log")

	l, closeLog, err := NewLogger(Options{Path: path}, slog.LevelInfo)
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	t.Cleanup(func() { _ = closeLog() })
	l.Info("hello", "k", "v")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("日志文件没建出来（父目录要自动建）: %v", err)
	}
	if !strings.Contains(string(got), "msg=hello") || !strings.Contains(string(got), "k=v") {
		t.Errorf("文件内容不对:\n%s", got)
	}
}

// 滚动是 lumberjack 干的，这里只验证 Options 真的传到了它手上。
// 阈值最小是 1MB，所以灌满 1MB+ 才能触发。
func TestLoggerRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.log")

	l, closeLog, err := NewLogger(Options{Path: path, MaxSizeMB: 1, MaxBackups: 2}, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLog() })

	pad := strings.Repeat("x", 4096)
	for range 400 { // 400 × ~4KB ≈ 1.6MB
		l.Info("pad", "data", pad)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("灌了 1.6MB 却没滚动，目录里只有 %v", names)
	}
}

func TestLoggerRejectsUnknownFormat(t *testing.T) {
	if _, _, err := NewLogger(Options{Format: "xml"}, slog.LevelInfo); err == nil {
		t.Fatal("NewLogger() = nil, want error")
	}
}

// json 格式走的是 slog 自己的 handler：数组天然就是数组，不需要多行展开
func TestLoggerJSONKeepsSliceAsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")

	l, closeLog, err := NewLogger(Options{Path: path, Format: "json"}, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLog() })
	l.Info("sql batch", "stmts", []string{"A;", "B;"})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"stmts":["A;","B;"]`) {
		t.Errorf("json 里 stmts 该是数组:\n%s", got)
	}
}
