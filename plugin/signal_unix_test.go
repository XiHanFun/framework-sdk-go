//go:build unix

package plugin

import (
	"reflect"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMShutsDown 核对 SIGTERM 与 stdin 关闭一样收尾（lifecycle.md §7.7）：调一次停止回调（reason 为 shutdown），Serve 返回 nil。
func TestSIGTERMShutsDown(t *testing.T) {
	h := start(t, defaultEnv())
	h.ready(deadline(t), nil)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := h.exited(5 * time.Second); err != nil {
		t.Errorf("serve returned %v", err)
	}
	if stops := read(h.seen, func() []string { return h.seen.stops }); !reflect.DeepEqual(stops, []string{"shutdown"}) {
		t.Errorf("the stop hook must run once with reason shutdown, got %v", stops)
	}
}
