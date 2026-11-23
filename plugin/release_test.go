package plugin

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/manifest"
	"github.com/xihanfun/framework-sdk-go/operation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestCallsReleaseTheirContext 核对 Invoke 与 HandleEvent 在结束时归还调用上下文：带截止时间的调用返回之后，
// 它的截止令牌不再置位，也就没有留到截止时间的定时器
func TestCallsReleaseTheirContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var seen []*invocation.Context
		registry := operation.NewRegistry()
		run := operation.HandlerFunc[struct{}, struct{}](func(ctx *invocation.Context, _ struct{}) primitives.Result[struct{}] {
			seen = append(seen, ctx)
			return primitives.Ok(struct{}{})
		})
		if err := operation.Add(registry, operation.Contract{ID: "probes.run", Stability: primitives.Experimental}, run, nil, ""); err != nil {
			t.Fatal(err)
		}
		invoker, err := operation.NewInvoker(registry, nil)
		if err != nil {
			t.Fatal(err)
		}
		m, err := manifest.Read([]byte(testManifest))
		if err != nil {
			t.Fatal(err)
		}
		handler := func(ctx *invocation.Context, _ Envelope, _ uint32) primitives.Result[primitives.Unit] {
			seen = append(seen, ctx)
			return primitives.Success()
		}
		r := &runtime{
			manifest:      m,
			invoker:       invoker,
			subscriptions: []SubscriptionHandler{{Subscription: Subscription{Topic: "probe.events"}, Handler: handler}},
			lifecycle:     newLifecycle(),
			calls:         newCalls(),
			logger:        slog.New(slog.DiscardHandler),
		}
		for _, state := range []primitives.LifecycleState{primitives.Resolved, primitives.Configured, primitives.Started, primitives.Ready} {
			if !r.lifecycle.transition(state, nil) {
				t.Fatalf("the transition to %s was refused", state)
			}
		}
		s := &service{r: r}
		metadata := &pluginv1.InvocationMetadata{Deadline: rules.FormatDeadline(time.Now().Add(time.Minute))}
		invoked, err := s.Invoke(context.Background(), &pluginv1.InvokeRequest{Operation: "probes.run", Metadata: metadata})
		if err != nil || !invoked.Status.Ok {
			t.Fatalf("invoke: %v %v", invoked, err)
		}
		delivered, err := s.HandleEvent(context.Background(), &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "probe.events", Id: "e1"}, Metadata: metadata})
		if err != nil || !delivered.Status.Ok {
			t.Fatalf("handle event: %v %v", delivered, err)
		}
		if len(seen) != 2 {
			t.Fatalf("the handlers saw %d contexts, want 2", len(seen))
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		for index, ctx := range seen {
			if !ctx.HasDeadline() || ctx.Err() != context.Canceled {
				t.Errorf("call %d: a finished call is cancelled and had a deadline, got %v", index, ctx.Err())
			}
			if ctx.DeadlineExceeded() {
				t.Errorf("call %d: the deadline token fired after the call had ended; its timer was not released", index)
			}
		}
	})
}
