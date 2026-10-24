package mail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"go_pg_test/app/test/logic/event"
	"go_pg_test/app/test/ptable"
	"go_pg_test/common/db"
	"go_pg_test/common/redis"
	"pdbgen/dbctx"
	"pdbgen/dbpool"
)

var ErrInvalidMail = errors.New("invalid mail")

// user_mail.confId 是 int32，GM 邮件用 -gm_mail.id 表示，所以 id 不能超 int32。
const maxUserMailConfID = int64(1<<31 - 1)

// GlobalMail 是全服 GM 邮件的输入。时间用 unix 秒，0 表示不限。
type GlobalMail struct {
	Params        string // json
	AwardList     string // json
	StartAt       int64
	EndAt         int64
	Channel       string
	UserCreatedAt int64
	Condition     string
}

// PublishGlobal 幂等地写入一封全服 GM 邮件。
//
// GM 平台把同一个 uuid 广播给所有服：每个服都会走到这里，谁先插入谁成功，
// 其他服在唯一索引上冲突后读出已有的 id，然后各自 Reload 自己的进程缓存。
// 事件版本由 GM 平台确认所有服都处理成功后统一 bump，不在这里做。
func PublishGlobal(ctx context.Context, uuid string, in GlobalMail) (mailID int64, created bool, err error) {
	if uuid == "" || len(uuid) > 128 {
		return 0, false, fmt.Errorf("%w: bad uuid", ErrInvalidMail)
	}
	// 最小实现不支持定时发布：事件版本只在发布方统一通知客户端一次。未来才生效的
	// 邮件如果现在通知，客户端会提前同步；到点又没人再通知。
	// 同样，channel/condition 需要渠道数据和脚本引擎，不能静默当成全服发。
	now := time.Now().Unix()
	if in.StartAt > now {
		return 0, false, fmt.Errorf("%w: startAt %d is in the future", ErrInvalidMail, in.StartAt)
	}
	if in.EndAt > 0 && in.EndAt < now {
		return 0, false, fmt.Errorf("%w: endAt %d is in the past", ErrInvalidMail, in.EndAt)
	}
	if in.Channel != "" || in.Condition != "" {
		return 0, false, fmt.Errorf("%w: channel/condition targeting is not implemented", ErrInvalidMail)
	}

	err = dbpool.WithTx(ctx, func(ctx context.Context) error {
		tx := dbctx.SQLTxFromCtx(ctx)
		const insert = `INSERT INTO "gm_mail" ` +
			`("uuid", "params", "award_list", "start_at", "end_at", "channel", "user_created_at", "condition") ` +
			`VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ` +
			`ON CONFLICT ("uuid") DO NOTHING RETURNING "id"`

		scanErr := tx.QueryRow(ctx, insert,
			uuid, in.Params, in.AwardList, in.StartAt, in.EndAt, in.Channel, in.UserCreatedAt, in.Condition,
		).Scan(&mailID)
		switch {
		case scanErr == nil:
			created = true
			if mailID <= 0 || mailID > maxUserMailConfID {
				return fmt.Errorf("gm_mail id %d cannot be represented in user_mail.conf_id", mailID)
			}
			return nil
		case errors.Is(scanErr, pgx.ErrNoRows):
			// 另一个服已经插入同一个 uuid。等它提交后把 id 读回来。
			created = false
			row := tx.QueryRow(ctx, `SELECT "id" FROM "gm_mail" WHERE "uuid" = $1`, uuid)
			if err := row.Scan(&mailID); err != nil {
				return fmt.Errorf("find gm_mail by uuid: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("insert gm_mail: %w", scanErr)
		}
	})
	if err != nil {
		return 0, false, err
	}

	// 这个服已经在事务里提交（或确认别人提交）了，刷新它自己的内存快照。
	if c := currentCache(); c != nil {
		if err := c.Reload(ctx); err != nil {
			return mailID, created, fmt.Errorf("mail %d saved, but reload cache failed: %w", mailID, err)
		}
	}
	return mailID, created, nil
}

// SendPrivate 直接给一个玩家投递邮件。只 bump 这个玩家的定向版本，
// 其他玩家的 PollEvents 看不到变化，因此不会去拉 CMails。
func SendPrivate(ctx context.Context, uid int64, params, awardList string) (mailID, eventSeq int64, err error) {
	var bumpErr error
	err = db.WithUserLock(ctx, uid, func(ctx context.Context) error {
		if ptable.UserTable.GetById(ctx, uid) == nil {
			return fmt.Errorf("user %d not found", uid)
		}

		m := ptable.NewMail()
		m.SetUid(uid)
		m.SetConfId(0) // 0：定向直投，不对应 gm_mail 模板
		m.SetState(MailStateNotRead)
		m.SetReceiveAt(time.Now().Unix())
		m.SetParams(params)
		m.SetAwardList(awardList)
		ptable.UserMailTable.Insert(ctx, m)
		mailID = m.Id()

		dbctx.AfterCommit(ctx, func(ctx context.Context) {
			eventSeq, bumpErr = redis.IncrUserEventVersion(ctx, uid, event.EventTypeMailNew)
		})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	if bumpErr != nil {
		return mailID, 0, fmt.Errorf("mail %d saved, but bump event failed: %w", mailID, bumpErr)
	}
	return mailID, eventSeq, nil
}
