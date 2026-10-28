package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

func New32() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash 是 token 落地（Redis key、数据库）时的样子：sha256 的 hex。
// token 本身是 32 字节随机数，直接 sha256 就够了，不用加盐或慢哈希；
// 存储泄露出去的只有哈希，拿去请求还会再被哈希一次，冒充不了任何人。
func Hash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}
