package main

import "go_pg_test/app/test/session"

// processRegistry 按协议号分发请求。登记都在 processor_registry.gen.go 里，
// 每个客户端协议一行 processRegistry.Handle(logic.ProcessXxx)。
// 处理方式（认证、事务、锁、requestToken 链）由处理函数 session 参数的类型决定，见 session 包。
var processRegistry = session.NewRegistry()
