package main

import "go_pg_test/common/server"

// session 在基础 Session 上挂业务层自己的 token 状态。
// 其他东西（认证、响应头）都直接用字段/方法在 handler 和 register 里写。
type session struct {
	*server.Session
	reqToken string
}
