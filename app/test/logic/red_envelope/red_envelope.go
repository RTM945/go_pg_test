package red_envelope

import (
	"context"
	"time"

	"go_pg_test/app/test/ptable"
	testpb "go_pg_test/genpb/test"
	"go_pg_test/io"
)

// resetHour 是每天换日的钟点：5 点之前算前一天。
const resetHour = 5

type RedEnvelope struct {
	*ptable.RedEnvelope
	readonly bool
}

// Get 取 uid 在活动 actId 里的红包记录，没有就新建一条（readonly 时只在内存里新建，不插入），
// 跨了游戏日就把今天的次数清零。readonly=false 要在持有 uid 锁的事务里调用（session.User 的处理函数），
// readonly=true 要在 db.Query 里调用。
func Get(ctx context.Context, uid int64, actId int32, readonly bool, now time.Time) *RedEnvelope {
	var redEnvelope *ptable.RedEnvelope
	if readonly {
		redEnvelope = ptable.UserRedEnvelopeTable.SelectByUidActId(ctx, uid, actId)
	} else {
		redEnvelope = ptable.UserRedEnvelopeTable.GetByUidActId(ctx, uid, actId)
	}
	if redEnvelope == nil {
		redEnvelope = ptable.NewRedEnvelope()
		redEnvelope.SetUid(uid)
		redEnvelope.SetActId(actId)
		redEnvelope.SetLastRefreshAt(now.Unix()) // 新记录算今天刷新过，下面的 refresh 不用再改一遍
		if !readonly {
			ptable.UserRedEnvelopeTable.Insert(ctx, redEnvelope)
		}
	}
	ret := &RedEnvelope{redEnvelope, readonly}
	ret.refresh(now)
	return ret
}

// refresh 跨了游戏日就把今天的次数清零。只读的记录不改：改了也不会落库，Select 出来的行还会 panic。
func (e *RedEnvelope) refresh(now time.Time) {
	if e.readonly || sameDay(time.Unix(e.LastRefreshAt(), 0), now) {
		return
	}
	e.SetTodayCount(0)
	e.SetLastRefreshAt(now.Unix())
}

// sameDay 报告 a 和 b 是不是同一个游戏日：每天 resetHour 点换日，按 b 所在的时区算。
func sameDay(a, b time.Time) bool {
	shift := resetHour * time.Hour
	ay, am, ad := a.In(b.Location()).Add(-shift).Date()
	by, bm, bd := b.Add(-shift).Date()
	return ay == by && am == bm && ad == bd
}

func (e *RedEnvelope) Online(session io.Session) error {
	session.Logger().Info("online", "uid", e.Uid(), "actId", e.ActId(), "todayCount", e.TodayCount(), "total", e.Total())

	return session.Write(&testpb.SRedEnvelope{
		ActId:         e.ActId(),
		LastRefreshAt: e.LastRefreshAt(),
		TodayCount:    e.TodayCount(),
		Total:         e.Total(),
	})
}

func (e *RedEnvelope) Click(session io.Session) error {
	session.Logger().Info("click", "uid", e.Uid(), "actId", e.ActId(), "todayCount", e.TodayCount(), "total", e.Total())
	e.SetTodayCount(e.TodayCount() + 1)
	e.SetTotal(e.Total() + 1)
	return e.Online(session)
}
