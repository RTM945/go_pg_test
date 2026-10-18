// Package session 是 app 层对 io.Session 的扩展：挂业务 token 状态。
// main 和 logic/login 都 import 它，避免在 login 侧再定义一个 setter 接口。
package session

import "go_pg_test/io"

type Session struct {
	io.Session

	reqToken       string
	nextLoginToken string
	nextReqToken   string
}

func New(base io.Session, reqToken string) *Session {
	return &Session{Session: base, reqToken: reqToken}
}

func (s *Session) ReqToken() string { return s.reqToken }

func (s *Session) SetNextLoginToken(v string) { s.nextLoginToken = v }
func (s *Session) SetNextReqToken(v string)   { s.nextReqToken = v }

func (s *Session) NextLoginToken() string { return s.nextLoginToken }
func (s *Session) NextReqToken() string   { return s.nextReqToken }
