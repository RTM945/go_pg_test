// Package session 是 app 层对 io.Session 的扩展：挂业务 token 状态，以及处理方式（mode.go）——
// 处理函数 session 参数的类型决定怎么认证、开不开事务、加什么锁。
// main 和 logic 都 import 它，避免在 login 侧再定义一个 setter 接口。
package session

import "go_pg_test/io"

type Session struct {
	io.Session

	loginToken     string // 请求带来的 loginToken，处理方式要认证时用它
	reqToken       string // 请求带来的 requestToken，session.User 用它维护单次链
	nextLoginToken string
	nextReqToken   string
}

// New 在传输层的 session 上挂业务状态。loginToken、reqToken 是请求带来的，没带就传空串。
func New(base io.Session, loginToken, reqToken string) *Session {
	return &Session{Session: base, loginToken: loginToken, reqToken: reqToken}
}

func (s *Session) SetNextLoginToken(v string) { s.nextLoginToken = v }
func (s *Session) SetNextReqToken(v string)   { s.nextReqToken = v }

func (s *Session) NextLoginToken() string { return s.nextLoginToken }
func (s *Session) NextReqToken() string   { return s.nextReqToken }
