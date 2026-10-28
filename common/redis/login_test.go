package redis

import (
	"strings"
	"testing"

	"go_pg_test/common/token"
)

// TestLoginKeyHidesToken Redis 里只有登录 token 的哈希：同一个 token 总是同一个 key，key 里看不到明文。
func TestLoginKeyHidesToken(t *testing.T) {
	tok := token.New32()
	key := loginKey(tok)
	if strings.Contains(key, tok) || key != loginKey(tok) || key == loginKey(token.New32()) {
		t.Fatalf("loginKey(%q) = %q", tok, key)
	}
}
