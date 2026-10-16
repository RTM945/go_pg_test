package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func newTestLogger(buf *bytes.Buffer, level slog.Leveler, attrs ...slog.Attr) *slog.Logger {
	h := NewHandler(buf, level).WithAttrs(attrs).(*Handler)
	return slog.New(h)
}

// 这是写这个 handler 的全部理由：SQL batch 的多条语句要各占一行。
func TestHandlerExpandsStringSlice(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelInfo)

	l.Info("sql batch", "cost", "900µs", "count", 2,
		"stmts", []string{"SET LOCAL statement_timeout = 5000;", "SELECT pg_try_advisory_xact_lock(1003);"})

	got := buf.String()

	for _, line := range []string{
		"SET LOCAL statement_timeout = 5000;",
		"SELECT pg_try_advisory_xact_lock(1003);",
	} {
		if !strings.Contains(got, "\n\t"+line) {
			t.Errorf("语句没有单独成行（要以 \\n\\t 开头）:\n%s", got)
		}
	}
	// 2 条语句展开成 2 个换行 + 结尾 1 个。不是每条语句一条记录。
	if n := strings.Count(got, "\n"); n != 3 {
		t.Errorf("换行数 = %d, want 3:\n%s", n, got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("记录要以换行结尾:\n%q", got)
	}
}

func TestHandlerBasicFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelInfo)

	l.Info("listening", "app", "lobby-svc", "addr", "127.0.0.1:8080", "count", 3)

	got := buf.String()
	// 不含空格的字符串不加引号，含空格的加——跟 slog.TextHandler 一个口径
	if !strings.Contains(got, "app=lobby-svc") {
		t.Errorf("不需要引号的值被加了引号:\n%s", got)
	}
	if !strings.Contains(got, "msg=listening") {
		t.Errorf("msg 没渲染出来:\n%s", got)
	}
	if !strings.Contains(got, "count=3") {
		t.Errorf("数字没渲染出来:\n%s", got)
	}
	if !strings.Contains(got, "level=INFO") {
		t.Errorf("level 没渲染出来:\n%s", got)
	}
}

func TestHandlerQuotesWhenNeeded(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelInfo)

	l.Info("sql batch", "stmt", "SELECT 1 WHERE a = 'b'")

	got := buf.String()
	if !strings.Contains(got, "msg=\"sql batch\"") {
		t.Errorf("含空格的 msg 该加引号:\n%s", got)
	}
	if !strings.Contains(got, `stmt="SELECT 1 WHERE a = 'b'"`) {
		t.Errorf("含空格和 = 的 stmt 该加引号:\n%s", got)
	}
	// 换行要被转义成两个字符，不能真的换行——只有 []string 才允许多行
	var buf2 bytes.Buffer
	newTestLogger(&buf2, slog.LevelInfo).Info("x", "v", "a\nb")
	if strings.Count(buf2.String(), "\n") != 1 {
		t.Errorf("普通字符串里的换行必须是转义的:\n%q", buf2.String())
	}
}

// With 加上的属性要排在记录自带属性前面，而且派生时不能丢
func TestHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := NewHandler(&buf, slog.LevelInfo).WithAttrs([]slog.Attr{slog.Int("uid", 1003)})

	slog.New(base).Info("click", "actId", 42)

	got := buf.String()
	if !strings.Contains(got, "uid=1003") {
		t.Errorf("With 的属性丢了:\n%s", got)
	}
	if strings.Index(got, "uid=1003") > strings.Index(got, "actId=42") {
		t.Errorf("With 的属性该排在记录属性前面:\n%s", got)
	}
}

func TestHandlerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelWarn)

	l.Info("不该出现")
	l.Warn("该出现")

	got := buf.String()
	if strings.Contains(got, "不该出现") {
		t.Errorf("Info 没被级别滤掉:\n%s", got)
	}
	if !strings.Contains(got, "该出现") {
		t.Errorf("Warn 被误滤了:\n%s", got)
	}
}

// 多行记录必须整体写出，否则并发时两条日志会在文件里交错
func TestHandlerConcurrentRecordsDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelInfo)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Info("batch", "stmts", []string{"A;", "B;", "C;"}, "n", i)
		}()
	}
	wg.Wait()

	// 每条记录都该是 "…stmts=\n\tA;\n\tB;\n\tC;\n" 这个形状，中间不能插进别的
	for _, rec := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		if strings.HasPrefix(rec, "\t") {
			continue // 展开出来的语句行
		}
		if !strings.Contains(rec, "msg=batch") {
			t.Errorf("有记录被拆开了: %q", rec)
		}
	}
	if n := strings.Count(buf.String(), "msg=batch"); n != 50 {
		t.Errorf("写出 %d 条，want 50", n)
	}
}
