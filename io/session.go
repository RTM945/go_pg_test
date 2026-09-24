package io

import "net/http"

type Session struct {
	UID int64
	W   http.ResponseWriter
}
