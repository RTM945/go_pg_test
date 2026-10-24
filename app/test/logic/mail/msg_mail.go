// 只创建一次，之后归你改，genpb 不会覆盖这个文件。
// source: test/mail.proto

package mail

import (
	"context"

	"go_pg_test/app/test/msg"
)

func ProcessCMails(ctx context.Context, req *msg.CMails) error {
	return processMails(ctx, req)
}

func ProcessCReadMail(ctx context.Context, req *msg.CReadMail) error {
	return processReadMail(ctx, req)
}

func ProcessCGetAttachment(ctx context.Context, req *msg.CGetAttachment) error {
	return processGetAttachment(ctx, req)
}
