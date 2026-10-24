package main

import (
	"go_pg_test/app/test/logic/event"
	"go_pg_test/app/test/logic/heartbeat"
	"go_pg_test/app/test/logic/login"
	"go_pg_test/app/test/msg"
	testpb "go_pg_test/genpb/test"
)

func init() {
	register(processRegistry, testpb.PROTOCOL_TYPE_Login, msg.NewLogin, login.ProcessLogin, processNone)
	// 心跳：要认证（续 Redis TTL），但不开事务、不消费 requestToken。
	register(processRegistry, testpb.PROTOCOL_TYPE_Heartbeat, msg.NewHeartbeat, heartbeat.ProcessHeartbeat, processAuthOnly)
	// 事件版本：认证 + 读 Redis 全局版本 hash；客户端本地保存版本 map。
	register(processRegistry, testpb.PROTOCOL_TYPE_PollEvents, msg.NewPollEvents, event.ProcessPollEvents, processAuthOnly)
	// 邮件协议（C 开头）由 processor_registry.gen.go 自动注册。
}
