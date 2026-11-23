package plugin

import (
	"reflect"
	"sync"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestHandlersSeeTheDeliveryMode 核对事件处理器经 EventDeliveryOf 拿到这次投递的方式与次数（invocation.md §4，HandleEventRequest.mode）：
// 空方式写成 competing；同一主题的竞争订阅与广播订阅各看到自己的方式；操作调用的上下文上没有投递信息
func TestHandlersSeeTheDeliveryMode(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	record := func(name string) EventHandler {
		return func(ctx *invocation.Context, envelope Envelope, attempt uint32) primitives.Result[primitives.Unit] {
			delivery, ok := EventDeliveryOf(ctx)
			// 派生的上下文共享对象袋，照样取得到
			derived, derivedOK := EventDeliveryOf(ctx.WithBaggage("xihan.feature-flag", "on"))
			mu.Lock()
			defer mu.Unlock()
			if !ok || !derivedOK || derived != delivery || delivery.Attempt != attempt {
				seen = append(seen, name+":"+envelope.ID+":missing")
				return primitives.Success()
			}
			seen = append(seen, name+":"+envelope.ID+":"+string(delivery.Mode))
			return primitives.Success()
		}
	}
	var inOperation []bool
	h := launch(t, defaultEnv(), func(opts *Options, probe *recorder) {
		opts.Subscriptions = []SubscriptionHandler{
			{Subscription: Subscription{Topic: "probe.events"}, Handler: record("competing")},
			{Subscription: Subscription{Topic: "probe.events", MessageType: "probe.cleared", Mode: SubscriptionModeBroadcast}, Handler: record("broadcast")},
		}
		probe.run = func(ctx *invocation.Context) *primitives.Error {
			_, ok := EventDeliveryOf(ctx)
			mu.Lock()
			inOperation = append(inOperation, ok)
			mu.Unlock()
			return nil
		}
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	deliver := func(id, mode string, attempt uint32) {
		t.Helper()
		response, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "probe.events", Id: id, Type: "probe.cleared", SpecVersion: "1.0"}, Mode: mode, Attempt: attempt})
		if err != nil || !response.Status.Ok {
			t.Fatalf("delivery %s in mode %q: %v %v", id, mode, response, err)
		}
	}
	deliver("e1", "", 1)
	deliver("e2", "competing", 2)
	deliver("e3", "broadcast", 3)
	if invoked, err := h.client.Invoke(ctx, &pluginv1.InvokeRequest{Operation: "probes.run"}); err != nil || !invoked.Status.Ok {
		t.Fatalf("invoke: %v %v", invoked, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"competing:e1:competing", "competing:e2:competing", "broadcast:e3:broadcast"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("handlers saw %v, want %v", seen, want)
	}
	if !reflect.DeepEqual(inOperation, []bool{false}) {
		t.Errorf("an operation invocation carries no delivery, got %v", inOperation)
	}
	if _, ok := EventDeliveryOf(invocation.Background()); ok {
		t.Error("a context outside the runtime carries no delivery")
	}
}
