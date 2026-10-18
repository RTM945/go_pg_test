package main

import (
	"go_pg_test/app/test/logic/login"
	"go_pg_test/app/test/msg"
	testpb "go_pg_test/genpb/test"
)

func init() {
	register(processRegistry, testpb.PROTOCOL_TYPE_Login, msg.NewLogin, login.ProcessLogin, processNone)
}
