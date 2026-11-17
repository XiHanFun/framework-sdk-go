//go:build windows

package plugin

import (
	"strings"
	"testing"
)

// TestServeListensOnANamedPipe 核对命名管道端点（handshake.md §4）：握手行合文法，当前用户能连上并握手。
func TestServeListensOnANamedPipe(t *testing.T) {
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) { opts.Network = NetworkPipe })
	if h.endpoint.Network != NetworkPipe || !strings.HasPrefix(h.endpoint.Address, `\\.\pipe\xihan-plugin-`) {
		t.Errorf("handshake line %q", h.line)
	}
	h.handshake(deadline(t))
}
