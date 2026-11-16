package operation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// TestKernelMessagesFollowTheVectors 回放 kernel-messages.json：每一项都由 SDK 的真实路径产出，错误码、消息键与参数名与向量一致，
// SDK 自带的 en-US 默认模板逐字相同，Error.Message 是模板按参数填好的文案
func TestKernelMessagesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.KernelMessages()
	if err != nil {
		t.Fatal(err)
	}
	produced := kernelErrors(t)
	for _, message := range vectors.Messages {
		template, ok := primitives.DefaultTemplate(message.Key)
		if !ok || template != message.Template {
			t.Errorf("%s: default template %q (registered %v), want %q", message.Key, template, ok, message.Template)
		}
		names := rules.Placeholders(message.Template)
		if !sameNames(names, message.Arguments) {
			t.Errorf("%s: the template uses %v, the vector lists %v", message.Key, names, message.Arguments)
		}
		err, ok := produced[message.Key]
		if !ok {
			t.Errorf("%s: the SDK does not produce this error in the test", message.Key)
			continue
		}
		if err.Code != message.Code || err.MessageKey != message.Key {
			t.Errorf("%s: produced %s / %s, want %s / %s", message.Key, err.Code, err.MessageKey, message.Code, message.Key)
		}
		var arguments []string
		for name := range err.Details {
			arguments = append(arguments, name)
		}
		if !sameNames(arguments, message.Arguments) {
			t.Errorf("%s: arguments %v, want %v", message.Key, arguments, message.Arguments)
		}
		if want, _ := primitives.TryFormatMessage(message.Template, err.Details); err.Message() != want {
			t.Errorf("%s: message %q, want %q", message.Key, err.Message(), want)
		}
		delete(produced, message.Key)
	}
	for key := range produced {
		t.Errorf("%s is produced by the SDK but missing from the vector", key)
	}
}

// kernelErrors 经 SDK 的真实路径产出向量里的每一项内核错误，按消息键索引
func kernelErrors(t *testing.T) map[string]*primitives.Error {
	t.Helper()
	produced := map[string]*primitives.Error{}
	record := func(err *primitives.Error) {
		if err == nil {
			t.Fatal("expected an error")
		}
		produced[err.MessageKey] = err
	}

	var uninitialized primitives.Result[int]
	record(uninitialized.Err())

	invoker := newInvoker(t, nil)
	record(invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.missing"}).Outcome.Err())
	record(invoker.Invoke(invocation.Background(), operation.Request{ID: "orders.create", Payload: []byte(`{"quantity":"two"}`)}).Outcome.Err())
	record(operation.Invoke[createOrder, string](invoker, invocation.Background(), "orders.create", createOrder{}).Err())
	record(invoker.Invoke(invocation.Background(invocation.WithDeadline(time.Now().Add(-time.Second))), operation.Request{ID: "orders.create"}).Outcome.Err())

	skip := pipeline.NewBuilder().UseAt(pipeline.Func("Skip", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] {
		return primitives.Success()
	}), primitives.Authorize, pipeline.End, "")
	record(newInvoker(t, skip).Invoke(invocation.Background(), operation.Request{ID: "orders.create"}).Outcome.Err())

	waiting := waitingInvoker(t)
	record(waiting.Invoke(invocation.Background(invocation.WithTimeout(10*time.Millisecond)), operation.Request{ID: "jobs.wait"}).Outcome.Err())
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	record(invocation.New(parent).Cancellation())

	registry := operation.NewRegistry()
	handler := operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { return primitives.Ok(struct{}{}) })
	for index := 0; index < operation.Capacity; index++ {
		if err := operation.Add(registry, operation.Contract{ID: fmt.Sprintf("items%d.get", index)}, handler, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	var registration *primitives.Error
	if !errors.As(operation.Add(registry, operation.Contract{ID: "items0.get"}, handler, nil, ""), &registration) {
		t.Fatal("duplicate")
	}
	record(registration)
	if !errors.As(operation.Add(registry, operation.Contract{ID: "items.overflow"}, handler, nil, ""), &registration) {
		t.Fatal("registry full")
	}
	record(registration)

	// 处理器 panic：调用器翻译为 kernel.invocation.unhandled_exception；panic 的日志写到丢弃的处理器
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previous)
	panicking := operation.NewRegistry()
	if err := operation.Add(panicking, operation.Contract{ID: "jobs.boom"}, operation.HandlerFunc[struct{}, struct{}](func(*invocation.Context, struct{}) primitives.Result[struct{}] { panic("boom") }), nil, ""); err != nil {
		t.Fatal(err)
	}
	panicker, err := operation.NewInvoker(panicking, nil)
	if err != nil {
		t.Fatal(err)
	}
	record(panicker.Invoke(invocation.Background(), operation.Request{ID: "jobs.boom"}).Outcome.Err())
	return produced
}

// sameNames 比较两组名字，不看顺序
func sameNames(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// counts 与 order 是 payload-reasons.json 的两个请求类型：成员值都是整数的对象，与 { lines: [ { quantity: 整数 } ] }
type counts map[string]int

type order struct {
	Lines []struct {
		Quantity int `json:"quantity"`
	} `json:"lines"`
}

// TestPayloadReasonsFollowTheVectors 回放 payload-reasons.json：载荷不合法时 reason 是期望的 JSON 路径，语法错误只要求以 $ 开头
func TestPayloadReasonsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.PayloadReasons()
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := operation.Add(registry, operation.Contract{ID: "counts.post"}, operation.HandlerFunc[counts, counts](func(_ *invocation.Context, request counts) primitives.Result[counts] { return primitives.Ok(request) }), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := operation.Add(registry, operation.Contract{ID: "order.post"}, operation.HandlerFunc[order, order](func(_ *invocation.Context, request order) primitives.Result[order] { return primitives.Ok(request) }), nil, ""); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	reason := func(request, payload string) (string, *primitives.Error) {
		failure := invoker.Invoke(invocation.Background(), operation.Request{ID: request + ".post", Payload: []byte(payload)}).Outcome.Err()
		if failure == nil || failure.Code != operation.ErrorCodeInvalidPayload || failure.MessageKey != operation.MessageInvalidPayload {
			return "", failure
		}
		text, _ := failure.Details["reason"].(string)
		return text, failure
	}
	for _, c := range vectors.Cases {
		if got, failure := reason(c.Request, c.Payload); got != c.Reason {
			t.Errorf("%s: %s reason %q, want %q (%v)", c.Name, c.Payload, got, c.Reason, failure)
		}
	}
	for _, c := range vectors.SyntaxErrors {
		if got, failure := reason(c.Request, c.Payload); !strings.HasPrefix(got, "$") {
			t.Errorf("syntax error %s: reason %q must start with $ (%v)", c.Payload, got, failure)
		}
	}
}
