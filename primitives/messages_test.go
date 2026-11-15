package primitives_test

import (
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestMessageKeyGrammar 核对 RFC-0001 / RFC-0010 的消息键文法：首段也可含下划线，最长 128 个字符
func TestMessageKeyGrammar(t *testing.T) {
	longest := "a." + strings.Repeat("b", primitives.MaxMessageKeyLength-2)
	for _, key := range []string{"a.b", "orders.quantity.invalid", "tenant_members.list.description", "kernel.operation.invalid_payload", "a1.b_2.c3", "a_.b_", longest} {
		if !primitives.IsMessageKey(key) {
			t.Errorf("%q must be valid", key)
		}
	}
	for _, key := range []string{"", "a", "a.", ".a", "a..b", "A.b", "a.B", "_a.b", "1a.b", "a.1b", "a-b.c", "a.b-c", "a.b ", " a.b", "a.b\n", longest + "b"} {
		if primitives.IsMessageKey(key) {
			t.Errorf("%q must be invalid", key)
		}
	}
	if primitives.MessageKeyDomain("tenant_members.list.description") != "tenant_members" || primitives.MessageKeyDomain("bad") != "" {
		t.Error("domain")
	}
}
