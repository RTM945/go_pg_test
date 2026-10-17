package io

import (
	"log/slog"
	"net/http"
)

// Session 是一次请求的最小句柄：怎么写响应、UID、logger。
//
// token、认证之类的业务状态不在这里。业务层要扩展就嵌入
// common/server.Session，再挂自己的字段。
type Session interface {
	Writer() http.ResponseWriter
	Logger() *slog.Logger
	UID() int64
	SetUID(int64)
	Write(Msg) error
	PendingFrames() int
	Flush() error
}
