package mail

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go_pg_test/app/test/ptable"
	"go_pg_test/app/test/session"
	"go_pg_test/common/dispatch"
	testpb "go_pg_test/genpb/test"
)

// 和 Java MailData 的状态值保持一致。
const (
	MailStateRead            int32 = 1 // 已读/已领取
	MailStateNotRead         int32 = 2 // 未读
	MailStateReadNotReceived int32 = 3 // 已读，附件未领取
)

var (
	ErrMailNotFound      = dispatch.NewClientError(dispatch.NotFound, "MAIL_NOT_FOUND", "mail not found")
	ErrNoAttachment      = dispatch.NewClientError(dispatch.Conflict, "NO_ATTACHMENT", "mail has no attachment")
	ErrAttachmentClaimed = dispatch.NewClientError(dispatch.Conflict, "ATTACHMENT_CLAIMED", "attachment already claimed")
)

// processMails 返回当前玩家的邮件。全服 GM 邮件在这里懒投递：
// 只有玩家主动同步时，才把当前生效的 gm_mail 复制到自己的 user_mail。
func processMails(ctx context.Context, s *session.User, _ *testpb.CMails) error {
	uid := s.UID()

	cache := currentCache()
	if cache == nil {
		return errors.New("mail cache not initialized")
	}
	if err := deliverGMMails(ctx, uid, cache.Configs()); err != nil {
		return err
	}

	mails := ptable.UserMailTable.ListByUid(ctx, uid)
	// 新邮件排前面。
	slices.SortFunc(mails, func(a, b *ptable.Mail) int {
		return cmp.Compare(b.Id(), a.Id())
	})

	resp := &testpb.SMails{Mails: make([]*testpb.MailData, 0, len(mails))}
	for _, m := range mails {
		resp.Mails = append(resp.Mails, &testpb.MailData{
			Key:       m.Id(),
			ConfId:    m.ConfId(),
			MailTime:  m.ReceiveAt(),
			State:     m.State(),
			Params:    m.Params(),
			AwardList: m.AwardList(),
		})
	}
	return s.Write(resp)
}

// processReadMail 标记已读，没有响应包。有附件时状态变 3，否则变 1。
func processReadMail(ctx context.Context, s *session.User, req *testpb.CReadMail) error {
	uid := s.UID()
	m := ptable.UserMailTable.GetById(ctx, req.Key)
	if m == nil || m.Uid() != uid {
		return ErrMailNotFound
	}

	if m.State() == MailStateNotRead || m.State() == 0 { // 0 兼容改动前插入的未读数据
		if hasAttachment(m.AwardList()) {
			m.SetState(MailStateReadNotReceived)
		} else {
			m.SetState(MailStateRead)
		}
	}
	return nil
}

// processGetAttachment 领取附件。当前没有道具系统，只做状态流转；
// 真正的发奖逻辑之后接在这里。
func processGetAttachment(ctx context.Context, s *session.User, req *testpb.CGetAttachment) error {
	uid := s.UID()
	m := ptable.UserMailTable.GetById(ctx, req.Key)
	if m == nil || m.Uid() != uid {
		return ErrMailNotFound
	}
	if err := checkClaimable(m.State(), m.AwardList()); err != nil {
		return err
	}
	m.SetState(MailStateRead)

	return s.Write(&testpb.SGetAttachment{Key: m.Id()})
}

// checkClaimable 附件只能领一次，而且得真有附件——接上发奖逻辑之后，重复领取就是重复发奖。
// 领过的邮件状态是 MailStateRead；没读就直接领也可以。
func checkClaimable(state int32, awardList string) error {
	switch {
	case !hasAttachment(awardList):
		return ErrNoAttachment
	case state == MailStateRead:
		return ErrAttachmentClaimed
	}
	return nil
}

func hasAttachment(awardList string) bool {
	s := strings.ReplaceAll(strings.TrimSpace(awardList), " ", "")
	return s != "" && s != "[]" && s != "null"
}

// deliverGMMails 把生效中的全服 GM 邮件复制到玩家邮箱。
// configs 来自进程缓存；GM 平台广播时每个服写完库都会刷新自己的缓存。
//
// 用 (uid, confId=-gm_mail.id) 去重，所以重复调用不会重复投递。
// gm_mail.channel / gm_mail.condition 需要渠道和脚本引擎，最小实现不判断；
// 走 PublishGlobal 发布时这两个字段非空会直接报错，避免误发给全服。
func deliverGMMails(ctx context.Context, uid int64, configs []Config) error {
	user := ptable.UserTable.GetById(ctx, uid)
	if user == nil {
		return fmt.Errorf("user %d not found", uid)
	}

	existing := ptable.UserMailTable.ListByUid(ctx, uid)
	seen := make(map[int32]struct{}, len(existing))
	for _, m := range existing {
		seen[m.ConfId()] = struct{}{}
	}

	now := time.Now().Unix()
	for _, gm := range configs {
		if gm.StartAt > 0 && now < gm.StartAt {
			continue
		}
		if gm.EndAt > 0 && now > gm.EndAt {
			continue
		}
		// 渠道/条件邮件需要额外数据，直接写库的这类邮件宁可不投，也不能误投给全服。
		if gm.Channel != "" || gm.Condition != "" {
			continue
		}
		// 0 表示不限创角时间；否则只发给在指定时间之前创角的玩家。
		if gm.UserCreatedAt > 0 && user.CreatedAt() > gm.UserCreatedAt {
			continue
		}
		if gm.ID <= 0 || gm.ID > maxUserMailConfID {
			continue
		}

		confID := -int32(gm.ID)
		if _, ok := seen[confID]; ok {
			continue
		}

		m := ptable.NewMail()
		m.SetUid(uid)
		m.SetConfId(confID)
		m.SetState(MailStateNotRead)
		m.SetReceiveAt(now)
		m.SetParams(gm.Params)
		m.SetAwardList(gm.AwardList)
		ptable.UserMailTable.Insert(ctx, m)
		seen[confID] = struct{}{}
	}
	return nil
}
