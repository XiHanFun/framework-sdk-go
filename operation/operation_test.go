package operation_test

import (
	"errors"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

type createOrder struct {
	Quantity int `json:"quantity"`
}

type orderCreated struct {
	ID       string `json:"id"`
	Quantity int    `json:"quantity"`
}

func newInvoker(t *testing.T, builder *pipeline.Builder) *operation.Invoker {
	t.Helper()
	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[createOrder, orderCreated](func(ctx *invocation.Context, request createOrder) primitives.Result[orderCreated] {
		if request.Quantity <= 0 {
			return primitives.Fail[orderCreated](primitives.Validation("ORDERS_VALIDATION_001", "orders.quantity.invalid", map[string]any{"quantity": request.Quantity}))
		}
		return primitives.Ok(orderCreated{ID: "o-" + ctx.Correlation(), Quantity: request.Quantity})
	})
	if err := operation.Add(registry, operation.Contract{ID: "orders.create", Stability: primitives.Stable, Expose: []string{operation.ExposeHTTP}}, handler, &operation.Schema{Request: "{}", Response: "{}"}, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, builder)
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

func TestInvokeRunsThePipelineAndEncodesTheResponse(t *testing.T) {
	var seen string
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Observe", func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		descriptor, _ := operation.Current(ctx)
		seen = descriptor.Contract.ID
		return next(ctx)
	}), primitives.Observe, pipeline.End, "")
	invoker := newInvoker(t, builder)
	ctx := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: "c1"}))

	response := invoker.Invoke(ctx, operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":2}`)})
	if !response.Outcome.OK() || string(response.Payload) != `{"id":"o-c1","quantity":2}` || seen != "orders.create" {
		t.Errorf("response %v %s seen %s", response.Outcome, response.Payload, seen)
	}

	failed := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":0}`)})
	if failed.Outcome.OK() || failed.Outcome.Err().Code != "ORDERS_VALIDATION_001" || failed.Payload != nil {
		t.Errorf("failed %v", failed.Outcome)
	}

	missing := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.missing"})
	if missing.Outcome.OK() || missing.Outcome.Err().Code != operation.ErrorCodeNotFound {
		t.Errorf("missing %v", missing.Outcome)
	}

	for _, payload := range []string{"null", "[1]", "{"} {
		invalid := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(payload)})
		if invalid.Outcome.OK() || invalid.Outcome.Err().Code != operation.ErrorCodeInvalidPayload {
			t.Errorf("payload %q: %v", payload, invalid.Outcome)
		}
	}
	empty := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create"})
	if empty.Outcome.OK() || empty.Outcome.Err().Code != "ORDERS_VALIDATION_001" {
		t.Errorf("an empty payload is {}: %v", empty.Outcome)
	}
}

func TestTypedInvokeChecksTypes(t *testing.T) {
	invoker := newInvoker(t, nil)
	ctx := invocation.Background(invocation.WithBaggage(map[string]string{primitives.BaggageCorrelation: "c2"}))
	created := operation.Invoke[createOrder, orderCreated](invoker, ctx, "orders.create", createOrder{Quantity: 3})
	if value, ok := created.Value(); !ok || value.ID != "o-c2" || value.Quantity != 3 {
		t.Errorf("typed %v", created)
	}
	mismatch := operation.Invoke[createOrder, string](invoker, invocation.Background(), "orders.create", createOrder{Quantity: 3})
	if mismatch.OK() || mismatch.Err().Code != operation.ErrorCodeTypeMismatch {
		t.Errorf("mismatch %v", mismatch)
	}
}

func TestRegistryRejectsDuplicatesAndBadIDs(t *testing.T) {
	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) })
	if err := operation.Add(registry, operation.Contract{ID: "Orders.Create"}, handler, nil, ""); err == nil {
		t.Error("bad ID")
	}
	if err := operation.Add(registry, operation.Contract{ID: "orders.create", Permission: "p", Policy: "q"}, handler, nil, ""); err == nil {
		t.Error("permission and policy are exclusive")
	}
	if err := operation.Add(registry, operation.Contract{ID: "orders.create"}, handler, nil, ""); err != nil {
		t.Fatal(err)
	}
	err := operation.Add(registry, operation.Contract{ID: "orders.create"}, handler, nil, "xihan.orders.other")
	var duplicate *primitives.Error
	if !errors.As(err, &duplicate) || duplicate.Code != operation.ErrorCodeDuplicate || duplicate.MessageKey != operation.MessageDuplicate || duplicate.Details["id"] != "orders.create" || duplicate.Details["owner"] != pipeline.AppOwner {
		t.Errorf("duplicate %v", err)
	}
	if duplicate != nil && duplicate.Message() != "Operation 'orders.create' is already registered by 'app'." {
		t.Errorf("duplicate message %q", duplicate.Message())
	}
	if list := registry.List(); len(list) != 1 || list[0].Owner != pipeline.AppOwner {
		t.Errorf("list %v", list)
	}
	// 操作 ID 的文法是 conventions/operation-ids.md 的 <resource>.<verb>（rules.IsOperationID）：连字符只在字母数字之间
	for _, id := range []string{"orders.create", "order-lines.list", "a1-b2.c3-d4"} {
		if !operation.IsID(id) {
			t.Errorf("%q is an operation ID", id)
		}
	}
	for _, id := range []string{"", "orders", "Orders.Create", "1orders.create", "orders-.create", "order--lines.list", "orders.create-", "orders.create.v2", "orders.create\n", "orders_lines.list"} {
		if operation.IsID(id) {
			t.Errorf("%q is not an operation ID", id)
		}
	}
}

// TestExposeNamesMatchTheProtocol 核对暴露的协议名与 plugin.proto 的 OperationDeclaration.expose 一致，含 ai
func TestExposeNamesMatchTheProtocol(t *testing.T) {
	names := []string{operation.ExposeHTTP, operation.ExposeGrpc, operation.ExposeMcp, operation.ExposeCommandLine, operation.ExposeAI}
	want := []string{"http", "grpc", "mcp", "commandline", "ai"}
	for index := range want {
		if names[index] != want[index] {
			t.Errorf("expose %d = %q, want %q", index, names[index], want[index])
		}
	}
	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) })
	if err := operation.Add(registry, operation.Contract{ID: "answers.ask", Expose: []string{operation.ExposeAI, operation.ExposeMcp}}, handler, nil, ""); err != nil {
		t.Fatal(err)
	}
	if descriptor, _ := registry.Get("answers.ask"); len(descriptor.Contract.Expose) != 2 || descriptor.Contract.Expose[0] != "ai" {
		t.Errorf("expose %v", descriptor.Contract.Expose)
	}
}
