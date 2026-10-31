// 只创建一次，之后归你改，genpb 不会覆盖这个文件。
// 处理方式由 session 参数的类型决定（见 go_pg_test/app/test/session），改类型就行。
// source: test/mail.proto

package mail

import (
	"context"

	"go_pg_test/app/test/session"
	testpb "go_pg_test/genpb/test"
)

func ProcessCMails(ctx context.Context, s *session.User, req *testpb.CMails) error {
	return processMails(ctx, s, req)
}

func ProcessCReadMail(ctx context.Context, s *session.User, req *testpb.CReadMail) error {
	return processReadMail(ctx, s, req)
}

func ProcessCGetAttachment(ctx context.Context, s *session.User, req *testpb.CGetAttachment) error {
	return processGetAttachment(ctx, s, req)
}
