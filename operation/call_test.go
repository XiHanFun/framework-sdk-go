package operation_test

import (
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

type countRequest struct{}

type countResponse struct {
	Count int `json:"count"`
}

// twoOperations 登记 orders.create 与 orders.count，并记录每次实际执行的操作
func twoOperations(t *testing.T, builder *pipeline.Builder, executed *[]string) *operation.Invoker {
	t.Helper()
	registry := operation.NewRegistry()
	create := operation.HandlerFunc[createOrder, orderCreated](func(_ *invocation.Context, request createOrder) primitives.Result[orderCreated] {
		*executed = append(*executed, "orders.create")
		return primitives.Ok(orderCreated{ID: "o1", Quantity: request.Quantity})
	})
	count := operation.HandlerFunc[countRequest, countResponse](func(*invocation.Context, countRequest) primitives.Result[countResponse] {
		*executed = append(*executed, "orders.count")
		return primitives.Ok(countResponse{Count: 7})
	})
	if err := operation.Add(registry, operation.Contract{ID: "orders.create"}, create, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := operation.Add(registry, operation.Contract{ID: "orders.count"}, count, nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, builder)
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

// TestTypedInvokeDoesNotLeakItsTerminalIntoLaterCalls 复现旧缺陷：类型化调用把终点放进对象袋后不清，
// 同一上下文后续的 JSON 调用会执行上一次的类型化终点
func TestTypedInvokeDoesNotLeakItsTerminalIntoLaterCalls(t *testing.T) {
	var executed []string
	invoker := twoOperations(t, nil, &executed)
	ctx := invocation.Background()

	created := operation.Invoke[createOrder, orderCreated](invoker, ctx, "orders.create", createOrder{Quantity: 2})
	if value, ok := created.Value(); !ok || value.Quantity != 2 {
		t.Fatalf("typed %v", created)
	}
	response := invoker.Invoke(ctx, operation.Request{ID: "orders.count"})
	if !response.Outcome.OK() || string(response.Payload) != `{"count":7}` {
		t.Errorf("the JSON call must run its own operation: %v %s", response.Outcome, response.Payload)
	}
	if len(executed) != 2 || executed[0] != "orders.create" || executed[1] != "orders.count" {
		t.Errorf("executed %v", executed)
	}
}

// TestInvokeRestoresTheItemsAfterTheCall 核对调用结束后对象袋恢复原样：当前操作、请求与响应只在调用期间可见
func TestInvokeRestoresTheItemsAfterTheCall(t *testing.T) {
	var executed []string
	invoker := twoOperations(t, nil, &executed)
	ctx := invocation.Background()
	ctx.Set("own", 1)

	invoker.Invoke(ctx, operation.Request{ID: "orders.count"})
	operation.Invoke[createOrder, orderCreated](invoker, ctx, "orders.create", createOrder{Quantity: 1})
	if _, ok := operation.Current(ctx); ok {
		t.Error("the current operation must not outlive the call")
	}
	if _, ok := operation.RequestOf(ctx); ok {
		t.Error("the request must not outlive the call")
	}
	if _, ok := operation.ResponseOf(ctx); ok {
		t.Error("the response must not outlive the call")
	}
	if value, ok := ctx.Get("own"); !ok || value != 1 {
		t.Error("the caller's own items stay")
	}
}

// TestNestedInvokeSeesItsOwnOperation 核对嵌套调用：中间件里再调另一个操作，内层看到自己的操作，返回后外层仍看到外层的
func TestNestedInvokeSeesItsOwnOperation(t *testing.T) {
	var executed, seen []string
	var invoker *operation.Invoker
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Nest", func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		descriptor, _ := operation.Current(ctx)
		seen = append(seen, "enter "+descriptor.Contract.ID)
		if descriptor.Contract.ID == "orders.create" {
			inner := invoker.Invoke(ctx, operation.Request{ID: "orders.count"})
			if string(inner.Payload) != `{"count":7}` {
				seen = append(seen, "inner payload "+string(inner.Payload))
			}
		}
		descriptor, _ = operation.Current(ctx)
		seen = append(seen, "before next "+descriptor.Contract.ID)
		return next(ctx)
	}), primitives.Observe, pipeline.End, "")
	invoker = twoOperations(t, builder, &executed)

	response := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":3}`)})
	if !response.Outcome.OK() || string(response.Payload) != `{"id":"o1","quantity":3}` {
		t.Errorf("outer response %v %s", response.Outcome, response.Payload)
	}
	want := []string{"enter orders.create", "enter orders.count", "before next orders.count", "before next orders.create"}
	if len(seen) != len(want) {
		t.Fatalf("seen %v", seen)
	}
	for index := range want {
		if seen[index] != want[index] {
			t.Errorf("seen %v, want %v", seen, want)
			break
		}
	}
}
