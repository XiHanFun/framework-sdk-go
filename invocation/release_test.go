package invocation_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xihanfun/framework-sdk-go/invocation"
)

// registrations 是数登记的父上下文：实现 context 包认的 AfterFunc 方法，context.WithDeadline、WithCancel 与 context.AfterFunc
// 都经它登记、注销时减一；它自己从不取消，相当于进程级的长寿命上下文
type registrations struct {
	context.Context
	done  chan struct{}
	count atomic.Int64
}

// newRegistrations 创建没有登记的父上下文
func newRegistrations() *registrations {
	return &registrations{Context: context.Background(), done: make(chan struct{})}
}

// Done 返回一个从不关闭的通道：有它 context 包才会在这个上下文上登记
func (r *registrations) Done() <-chan struct{} { return r.done }

// AfterFunc 记一个登记，返回的函数注销它
func (r *registrations) AfterFunc(func()) func() bool {
	r.count.Add(1)
	var once sync.Once
	return func() bool {
		stopped := false
		once.Do(func() {
			r.count.Add(-1)
			stopped = true
		})
		return stopped
	}
}

// TestNewLeavesNothingOnTheParentAfterTheDeadline 核对不带释放入口的 New：截止时间一过，长寿命的 parent 上不再留它的登记
func TestNewLeavesNothingOnTheParentAfterTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent := newRegistrations()
		ctx := invocation.New(parent, invocation.WithTimeout(time.Second))
		if parent.count.Load() == 0 {
			t.Fatal("a pending deadline must be registered on the parent")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if err := ctx.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded || !ctx.DeadlineExceeded() {
			t.Errorf("after the deadline: %v", err)
		}
		if left := parent.count.Load(); left != 0 {
			t.Errorf("%d registrations are left on the parent after the deadline", left)
		}
	})
}

// TestNewWithCancelReleasesTheParentAndTheTimer 核对释放入口：释放之后 parent 上没有登记、上下文以取消结束、截止令牌不再置位，
// 重复释放无害
func TestNewWithCancelReleasesTheParentAndTheTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent := newRegistrations()
		ctx, cancel := invocation.NewWithCancel(parent, invocation.WithTimeout(time.Hour))
		if parent.count.Load() == 0 || ctx.Err() != nil {
			t.Fatalf("before the release: %d registrations, err %v", parent.count.Load(), ctx.Err())
		}
		cancel()
		if left := parent.count.Load(); left != 0 {
			t.Errorf("%d registrations are left on the parent after the release", left)
		}
		if err := ctx.Cancellation(); ctx.Err() != context.Canceled || err == nil || err.Code != invocation.ErrorCodeCancelled {
			t.Errorf("a released context is cancelled, got %v (%v)", ctx.Err(), err)
		}
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if ctx.DeadlineExceeded() {
			t.Error("the deadline token must not fire after the release")
		}
		cancel()

		// 没有截止时间的上下文同样可以释放
		plain, cancel := invocation.NewWithCancel(parent)
		if plain.HasDeadline() || parent.count.Load() != 1 {
			t.Errorf("without a deadline: %d registrations", parent.count.Load())
		}
		cancel()
		if plain.Err() != context.Canceled || parent.count.Load() != 0 {
			t.Errorf("after the release: err %v, %d registrations", plain.Err(), parent.count.Load())
		}
	})
}

// TestReleaseStopsTheTokenOfACancelledCall 核对调用方先取消的情形：不释放时截止令牌照样在截止时间置位（归因用它），
// 释放之后不再置位、也不留定时器
func TestReleaseStopsTheTokenOfACancelledCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		kept, releaseKept := invocation.NewWithCancel(parent, invocation.WithTimeout(time.Minute))
		defer releaseKept()
		released, release := invocation.NewWithCancel(parent, invocation.WithTimeout(time.Minute))
		cancelParent()
		synctest.Wait()
		release()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if err := kept.Cancellation(); err == nil || err.Code != invocation.ErrorCodeDeadlineExceeded {
			t.Errorf("without a release the token still fires at the deadline, got %v", err)
		}
		if err := released.Cancellation(); released.DeadlineExceeded() || err == nil || err.Code != invocation.ErrorCodeCancelled {
			t.Errorf("after the release the token must not fire, got %v", err)
		}
	})
}
