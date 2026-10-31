package heartbeat

import (
	"context"

	"go_pg_test/app/test/session"
	testpb "go_pg_test/genpb/test"
)

// ProcessCHeartbeat 不写任何响应：HTTP 200 本身就是心跳成功的信号。
// 认证和 Redis TTL 续期由 session.Auth 在调用之前做完。
//
// 事件变化由客户端通过 CPollEvents 拉取版本号，不在心跳里发送。
func ProcessCHeartbeat(context.Context, *session.Auth, *testpb.CHeartbeat) error {
	return nil
}
