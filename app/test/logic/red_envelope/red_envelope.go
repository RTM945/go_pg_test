package red_envelope

import (
	"context"
	"time"

	"go_pg_test/app/test/msg"
	"go_pg_test/app/test/ptable"
	"go_pg_test/io"
)

type RedEnvelope struct {
	*ptable.RedEnvelope
	readonly bool
}

func Get(ctx context.Context, uid int64, actId int32, readonly bool) *RedEnvelope {
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
		redEnvelope.SetTodayCount(0)
		redEnvelope.SetTotal(0)
		redEnvelope.SetLastRefreshAt(0)
		if !readonly {
			ptable.UserRedEnvelopeTable.Insert(ctx, redEnvelope)
		}
	}
	ret := &RedEnvelope{redEnvelope, readonly}
	ret.refresh()
	return ret
}

func (e *RedEnvelope) refresh() {
	if e.readonly {
		return
	}
	//if !timeutil.IsSameDay(time.Now, e.LastRefreshAt(), 5) {
	// 跨天刷新次数
	e.SetTodayCount(0)
	e.SetLastRefreshAt(time.Now().Unix())
	//}
}

func (e *RedEnvelope) Online(session io.Session) error {
	session.Logger().Info("online", "uid", e.Uid(), "actId", e.ActId(), "todayCount", e.TodayCount(), "total", e.Total())

	resp := msg.NewSRedEnvelope()
	resp.ActId = e.ActId()
	resp.LastRefreshAt = e.LastRefreshAt()
	resp.TodayCount = e.TodayCount()
	resp.Total = e.Total()
	return session.Write(resp)
}

func (e *RedEnvelope) Click(session io.Session) error {
	session.Logger().Info("click", "uid", e.Uid(), "actId", e.ActId(), "todayCount", e.TodayCount(), "total", e.Total())
	e.SetTodayCount(e.TodayCount() + 1)
	e.SetTotal(e.Total() + 1)
	return e.Online(session)
}
