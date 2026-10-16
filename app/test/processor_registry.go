package main

import (
	"go_pg_test/app/test/login"
	"go_pg_test/app/test/msg"
	"go_pg_test/common/server"
	testpb "go_pg_test/genpb/test"
)

func init() {
	processRegistry.Register(testpb.PROTOCOL_TYPE_Login, msg.NewLogin, login.ProcessLogin, server.ProcessNone)
}
