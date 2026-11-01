package red_envelope

import (
	"log/slog"
	"testing"
	"time"

	"go_pg_test/app/test/ptable"
	testpb "go_pg_test/genpb/test"
	"go_pg_test/io"
)

func TestSameDay(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 0, 0, 0, loc) }
	for _, tc := range []struct {
		a, b time.Time
		want bool
	}{
		{at(1, 6), at(1, 23), true},
		{at(1, 4), at(1, 6), false}, // 5 点换日
		{at(1, 23), at(2, 4), true}, // 第二天 5 点之前还算前一天
		{at(1, 23), at(2, 5), false},
		{at(1, 6).UTC(), at(1, 23), true}, // 按 b 的时区算，a 是哪个时区的值都一样
	} {
		if got := sameDay(tc.a, tc.b); got != tc.want {
			t.Errorf("sameDay(%v, %v) = %v，want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestRefreshAndClick 每个请求都先 refresh 再领：同一个游戏日里次数累加，跨天清零、总数保留。
// 用 NewRedEnvelope 建的新对象不需要事务，可以直接测业务逻辑。
func TestRefreshAndClick(t *testing.T) {
	day1 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	r := ptable.NewRedEnvelope()
	r.SetLastRefreshAt(day1.Unix())
	e := &RedEnvelope{RedEnvelope: r}
	s := &recordingSession{}

	for _, now := range []time.Time{day1.Add(time.Hour), day1.Add(2 * time.Hour)} {
		e.refresh(now)
		if err := e.Click(s); err != nil {
			t.Fatal(err)
		}
	}
	if e.TodayCount() != 2 || e.Total() != 2 {
		t.Fatalf("同一天领两次，today=%d total=%d，want 2 2", e.TodayCount(), e.Total())
	}
	if last := s.sent[len(s.sent)-1].(*testpb.SRedEnvelope); last.TodayCount != 2 || last.Total != 2 {
		t.Fatalf("下发的 SRedEnvelope 是 %v，want todayCount=2 total=2", last)
	}

	day2 := day1.Add(24 * time.Hour)
	e.refresh(day2)
	if e.TodayCount() != 0 || e.Total() != 2 || e.LastRefreshAt() != day2.Unix() {
		t.Fatalf("跨天应该清零今天的次数、保留总数，today=%d total=%d lastRefreshAt=%d",
			e.TodayCount(), e.Total(), e.LastRefreshAt())
	}
}

// recordingSession 记下发出去的消息。内嵌的 io.Session 是 nil：调到没实现的方法直接 panic。
type recordingSession struct {
	io.Session
	sent []io.Msg
}

func (s *recordingSession) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func (s *recordingSession) Write(m io.Msg) error {
	s.sent = append(s.sent, m)
	return nil
}
