// Package logging 是本项目的日志落地方式：一个自定义的 slog handler，
// 加上一个按大小滚动的 writer。
//
// 为什么不直接用 slog.TextHandler：它把换行转义成 "\n" 两个字符，一条记录
// 就是一行。SQL batch 那种"一次往返里排了好几条语句"的场景，行内拼成
// "[a; b; c;]" 或者 "a;\nb;" 都没法读——那是当初特意排成多行的。
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Handler 输出格式和 slog.TextHandler 一致，只多一条规则：
//
//	值是 []string 的属性按行展开，每行缩进一级。
//
// 别的都照常——标量、时间、级别名、以及"需要时才加引号"的规则。
type Handler struct {
	// mu 由所有派生出来的 handler 共享，保证一条记录整体写出，
	// 不然并发的两条日志会在文件里交错。
	mu    *sync.Mutex
	w     io.Writer
	level slog.Leveler
	attrs []slog.Attr // 来自 With，渲染时排在记录自带的属性前面
	group string      // WithGroup 的前缀，本项目没用到但得正确传递
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler 建一个写向 w 的 handler。level 可以传 *slog.LevelVar，
// 那样运行期改级别能立刻生效。
func NewHandler(w io.Writer, level slog.Leveler) *Handler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &Handler{mu: new(sync.Mutex), w: w, level: level}
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	appendKey(&b, "time")
	b.WriteByte('=')
	b.WriteString(r.Time.Format(time.RFC3339Nano))
	b.WriteByte(' ')
	appendKey(&b, "level")
	b.WriteByte('=')
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	appendKey(&b, "msg")
	b.WriteByte('=')
	appendString(&b, r.Message)

	for _, a := range h.attrs {
		h.appendAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, a)
		return true
	})

	b.WriteByte('\n')

	// 整条记录一次写出：多行展开之后中间不能插进别的记录
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// 派生出来的 handler 共享 mu 和 w，只多一批属性
	nh := *h
	nh.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &nh
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	if nh.group == "" {
		nh.group = name
	} else {
		nh.group += "." + name
	}
	return &nh
}

func (h *Handler) appendAttr(b *strings.Builder, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}

	// WithGroup 里的分组本身：值是个 group，展平成 "组.键"
	if a.Value.Kind() == slog.KindGroup {
		saved := h.group
		h.group = key
		for _, ga := range a.Value.Group() {
			h.appendAttr(b, ga)
		}
		h.group = saved
		return
	}

	b.WriteByte(' ')
	appendKey(b, key)
	b.WriteByte('=')

	// 就是这一条：[]string 按行展开
	if ss, ok := a.Value.Any().([]string); ok {
		for _, line := range ss {
			b.WriteString("\n\t")
			b.WriteString(line)
		}
		return
	}

	appendValue(b, a.Value)
}

func appendKey(b *strings.Builder, s string) {
	if needsQuoting(s) {
		b.WriteString(strconv.Quote(s))
		return
	}
	b.WriteString(s)
}

func appendString(b *strings.Builder, s string) {
	if needsQuoting(s) {
		b.WriteString(strconv.Quote(s))
		return
	}
	b.WriteString(s)
}

func appendValue(b *strings.Builder, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		appendString(b, v.String())
	case slog.KindTime:
		b.WriteString(v.Time().Format(time.RFC3339Nano))
	case slog.KindDuration:
		b.WriteString(v.Duration().String())
	case slog.KindBool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case slog.KindInt64:
		b.WriteString(strconv.FormatInt(v.Int64(), 10))
	case slog.KindUint64:
		b.WriteString(strconv.FormatUint(v.Uint64(), 10))
	case slog.KindFloat64:
		b.WriteString(strconv.FormatFloat(v.Float64(), 'g', -1, 64))
	case slog.KindAny:
		appendString(b, fmt.Sprint(v.Any()))
	default:
		appendString(b, v.String())
	}
}

// needsQuoting 照着 slog.TextHandler 的口径：空串要引，含空白/=/" /\ 要引，
// 不可打印字符要引。中文是可打印的，不加引号——和 TextHandler 一致。
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == '=' || r == '"' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}
