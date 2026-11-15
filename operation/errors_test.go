package operation_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestKernelTemplatesMatchTheReferenceImplementation 核对内核消息键的 en-US 默认模板逐字照 .NET 的语言文件
func TestKernelTemplatesMatchTheReferenceImplementation(t *testing.T) {
	want := map[string]string{
		"kernel.operation.unknown":                "Operation '{id}' is not registered.",
		"kernel.operation.invalid_payload":        "The payload of operation '{id}' is not valid: {reason}",
		"kernel.operation.type_mismatch":          "Operation '{id}' handles '{expected}', but '{actual}' was supplied.",
		"kernel.operation.no_response":            "Operation '{id}' completed without producing a response.",
		"kernel.operation.duplicate":              "Operation '{id}' is already registered by '{owner}'.",
		"kernel.operation.registry_full":          "The operation registry has reached its capacity of {capacity}.",
		"kernel.invocation.deadline_exceeded":     "The invocation exceeded its deadline.",
		"kernel.invocation.deadline_before_start": "The invocation exceeded its deadline before it started.",
		"kernel.invocation.cancelled":             "The invocation was canceled.",
		"kernel.invocation.unhandled_exception":   "An unhandled exception occurred during the invocation.",
		"primitives.result.uninitialized":         "The result is uninitialized: a default value is not a valid result.",
	}
	for key, template := range want {
		if got, ok := primitives.DefaultTemplate(key); !ok || got != template {
			t.Errorf("%s: %q (registered %v), want %q", key, got, ok, template)
		}
	}
	keys := map[string]string{
		operation.MessageNotFound: "kernel.operation.unknown", operation.MessageInvalidPayload: "kernel.operation.invalid_payload",
		operation.MessageTypeMismatch: "kernel.operation.type_mismatch", operation.MessageNoResponse: "kernel.operation.no_response",
		operation.MessageDuplicate: "kernel.operation.duplicate", operation.MessageRegistryFull: "kernel.operation.registry_full",
		invocation.MessageDeadlineExceeded: "kernel.invocation.deadline_exceeded", invocation.MessageDeadlineBeforeStart: "kernel.invocation.deadline_before_start",
		invocation.MessageCancelled: "kernel.invocation.cancelled", primitives.MessageUninitializedResult: "primitives.result.uninitialized",
	}
	for got, want := range keys {
		if got != want {
			t.Errorf("key %q, want %q", got, want)
		}
	}
}

// TestUnknownOperation 核对未登记：KERNEL_NOTFOUND_001 + kernel.operation.unknown，参数 {id}
func TestUnknownOperation(t *testing.T) {
	invoker := newInvoker(t, nil)
	assertError(t, invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.missing"}).Outcome.Err(),
		"KERNEL_NOTFOUND_001", "kernel.operation.unknown", map[string]any{"id": "orders.missing"}, "Operation 'orders.missing' is not registered.")
	assertError(t, operation.Invoke[createOrder, orderCreated](invoker, invocation.Background(), "orders.missing", createOrder{}).Err(),
		"KERNEL_NOTFOUND_001", "kernel.operation.unknown", map[string]any{"id": "orders.missing"}, "Operation 'orders.missing' is not registered.")
}

type orderLine struct {
	Quantity int `json:"quantity"`
}

type bulkOrder struct {
	Lines  []orderLine          `json:"lines"`
	Labels map[string]orderLine `json:"labels"`
}

// TestInvalidPayloadReasonIsOnlyAPath 核对载荷不合法：KERNEL_VALIDATION_001 + kernel.operation.invalid_payload，参数 {id, reason}，
// reason 只写出错位置的 JSON 路径、不带类型名（RFC-0011 规则 2）
func TestInvalidPayloadReasonIsOnlyAPath(t *testing.T) {
	registry := operation.NewRegistry()
	echo := operation.HandlerFunc[bulkOrder, bulkOrder](func(_ *invocation.Context, request bulkOrder) primitives.Result[bulkOrder] {
		return primitives.Ok(request)
	})
	if err := operation.Add(registry, operation.Contract{ID: "orders.bulk"}, echo, nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker := newInvoker(t, nil)
	bulk, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		invoker *operation.Invoker
		id      string
		payload string
		reason  string
	}{
		{invoker, "orders.create", `null`, "the payload is null"},
		{invoker, "orders.create", ` null `, "the payload is null"},
		{invoker, "orders.create", `[1]`, "$"},
		{invoker, "orders.create", `{`, "$"},
		{invoker, "orders.create", `"text"`, "$"},
		{invoker, "orders.create", `{"quantity":"two"}`, "$.quantity"},
		{invoker, "orders.create", `{"quantity":1.5}`, "$.quantity"},
		{invoker, "orders.create", `{"other":1,"quantity":{}}`, "$.quantity"},
		{bulk, "orders.bulk", `{"lines":[{"quantity":1},{"quantity":"x"}]}`, "$.lines[1].quantity"},
		{bulk, "orders.bulk", `{"lines":{}}`, "$.lines"},
		{bulk, "orders.bulk", `{"lines":[1]}`, "$.lines[0]"},
		{bulk, "orders.bulk", `{"labels":{"a b":{"quantity":true}}}`, "$.labels['a b'].quantity"},
		{bulk, "orders.bulk", `{"labels":{"main":{"quantity":[]}}}`, "$.labels.main.quantity"},
	}
	for _, c := range cases {
		response := c.invoker.Invoke(invocation.Background(), operation.Request{ID: c.id, Payload: []byte(c.payload)})
		assertError(t, response.Outcome.Err(), "KERNEL_VALIDATION_001", "kernel.operation.invalid_payload", map[string]any{"id": c.id, "reason": c.reason},
			fmt.Sprintf("The payload of operation '%s' is not valid: %s", c.id, c.reason))
	}
	if got := operation.InvalidPayload("orders.create", "$.quantity"); got.Code != operation.ErrorCodeInvalidPayload || got.Details["reason"] != "$.quantity" {
		t.Errorf("InvalidPayload %v", got)
	}
}

// TestTypeMismatchNamesBothSignatures 核对类型化调用不符：KERNEL_VALIDATION_002 + kernel.operation.type_mismatch，参数 {id, expected, actual}
func TestTypeMismatchNamesBothSignatures(t *testing.T) {
	invoker := newInvoker(t, nil)
	assertError(t, operation.Invoke[createOrder, string](invoker, invocation.Background(), "orders.create", createOrder{Quantity: 1}).Err(),
		"KERNEL_VALIDATION_002", "kernel.operation.type_mismatch", map[string]any{"id": "orders.create", "expected": "createOrder -> orderCreated", "actual": "createOrder -> string"},
		"Operation 'orders.create' handles 'createOrder -> orderCreated', but 'createOrder -> string' was supplied.")
	assertError(t, operation.Invoke[map[string]any, *orderCreated](invoker, invocation.Background(), "orders.create", nil).Err(),
		"KERNEL_VALIDATION_002", "kernel.operation.type_mismatch", map[string]any{"id": "orders.create", "expected": "createOrder -> orderCreated", "actual": "map[string]interface {} -> *operation_test.orderCreated"},
		"Operation 'orders.create' handles 'createOrder -> orderCreated', but 'map[string]interface {} -> *operation_test.orderCreated' was supplied.")
}

// TestShortCircuitWithSuccessHasNoResponse 核对中间件以成功短路、处理器没有产出响应：KERNEL_UNEXPECTED_001 + kernel.operation.no_response，参数 {id}；
// 旧版 JSON 调用返回 OK 加空载荷
func TestShortCircuitWithSuccessHasNoResponse(t *testing.T) {
	builder := pipeline.NewBuilder().UseAt(pipeline.Func("Skip", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] {
		return primitives.Success()
	}), primitives.Authorize, pipeline.End, "")
	invoker := newInvoker(t, builder)
	response := invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":1}`)})
	if response.Payload != nil {
		t.Errorf("payload %s", response.Payload)
	}
	assertError(t, response.Outcome.Err(), "KERNEL_UNEXPECTED_001", "kernel.operation.no_response", map[string]any{"id": "orders.create"}, "Operation 'orders.create' completed without producing a response.")
	assertError(t, operation.Invoke[createOrder, orderCreated](invoker, invocation.Background(), "orders.create", createOrder{Quantity: 1}).Err(),
		"KERNEL_UNEXPECTED_001", "kernel.operation.no_response", map[string]any{"id": "orders.create"}, "Operation 'orders.create' completed without producing a response.")
}

// TestRegistryFull 核对登记表已满：KERNEL_OPERATION_002 + kernel.operation.registry_full，参数 {capacity}
func TestRegistryFull(t *testing.T) {
	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) })
	for index := 0; index < operation.Capacity; index++ {
		if err := operation.Add(registry, operation.Contract{ID: fmt.Sprintf("items%d.get", index)}, handler, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	err := operation.Add(registry, operation.Contract{ID: "items.overflow"}, handler, nil, "")
	var full *primitives.Error
	if !errors.As(err, &full) {
		t.Fatalf("full %v", err)
	}
	assertError(t, full, "KERNEL_OPERATION_002", "kernel.operation.registry_full", map[string]any{"capacity": operation.Capacity}, "The operation registry has reached its capacity of 10000.")
}

// TestUninitializedResult 核对未初始化结果：PRIMITIVES_VALIDATION_002 + primitives.result.uninitialized
func TestUninitializedResult(t *testing.T) {
	var result primitives.Result[int]
	assertError(t, result.Err(), "PRIMITIVES_VALIDATION_002", "primitives.result.uninitialized", nil, "The result is uninitialized: a default value is not a valid result.")
}

// assertError 核对错误码、消息键、参数与 en-US 文案
func assertError(t *testing.T, err *primitives.Error, code, key string, details map[string]any, message string) {
	t.Helper()
	if err == nil {
		t.Errorf("expected %s, got no error", code)
		return
	}
	if err.Code != code || err.MessageKey != key {
		t.Errorf("error %s / %s, want %s / %s", err.Code, err.MessageKey, code, key)
	}
	if len(err.Details) != len(details) {
		t.Errorf("details %v, want %v", err.Details, details)
	}
	for name, value := range details {
		if err.Details[name] != value {
			t.Errorf("detail %s = %#v, want %#v", name, err.Details[name], value)
		}
	}
	if err.Message() != message {
		t.Errorf("message %q, want %q", err.Message(), message)
	}
}
