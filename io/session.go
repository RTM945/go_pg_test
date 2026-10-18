package io

import "log/slog"

// Session 是一次请求的最小句柄：收消息、发消息、UID、logger。
//
// 传输相关的东西（HTTP header、TCP 控制帧、gRPC metadata）不在这里。
// 业务层要扩展就嵌 io.Session，再挂自己的字段；具体传输的 handler
// 负责把这些字段按自己的协议发出去。
type Session interface {
	Logger() *slog.Logger
	UID() int64
	SetUID(int64)
	Write(Msg) error
	PendingFrames() int
	Flush() error
}
