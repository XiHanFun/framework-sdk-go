package operation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/manifest"
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

	// 清单读取失败（RFC-0002 解析规则 2，ADR-0236）：错误码都是 KERNEL_CAPABILITY_006，message 是 en-US 模板的句子
	readManifest := func(data string) *primitives.Error {
		var failure *primitives.Error
		if _, err := manifest.Read([]byte(data)); !errors.As(err, &failure) {
			t.Fatalf("%s: expected *primitives.Error, got %v", data, err)
		}
		return failure
	}
	const base = `"id":"xihan.a.b","version":"1.0.0","primitives":"1","stability":"stable"`
	record(readManifest(`{`))
	record(readManifest(`null`))
	record(readManifest(`{}`))
	record(readManifest(`{` + base + `,"kind":"plugin"}`))
	record(readManifest(`{` + base + `,"kind":"capability","isolation":"wasm"}`))
	record(readManifest(`{` + base + `,"kind":"capability","provides":[{"id":"a.b","version":"1"},{"id":"a.b","version":"2"}]}`))
	record(readManifest(`{` + base + `,"kind":"capability","requires":[{"id":"a.b","version":"1"},{"id":"a.b","version":"1"}]}`))
	record(readManifest(`{` + base + `,"kind":"capability","bindings":[{"name":"db","type":"queue"},{"name":"db","type":"queue"}]}`))
	var missing *primitives.Error
	if _, err := manifest.ReadFile(filepath.Join(t.TempDir(), "absent.json")); !errors.As(err, &missing) {
		t.Fatalf("missing manifest: %v", err)
	}
	record(missing)
	return produced
}

// sameNames 比较两组名字，不看顺序
func sameNames(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// counts、order 与 profile 是 payload-reasons.json 的三个请求类型（requests 的描述）：成员值都是 32 位整数的对象；
// { lines: [ { quantity: 32 位整数 } ] }，都可选；profile 的 name 与 inner.n 必填（xihan:"required"），其余可选
type counts map[string]int32

type order struct {
	Lines []struct {
		Quantity int32 `json:"quantity"`
	} `json:"lines"`
}

type profile struct {
	Name   string   `json:"name" xihan:"required"`
	Note   string   `json:"note"`
	Count  int64    `json:"count"`
	Ratio  float64  `json:"ratio"`
	Flag   bool     `json:"flag"`
	Tags   []string `json:"tags"`
	Scores []int64  `json:"scores"`
	Inner  *struct {
		N int64 `json:"n" xihan:"required"`
	} `json:"inner"`
	Meta any `json:"meta"`
}

// echo 登记一个只解码请求的操作：向量只核对解码，响应是空对象（1e400 读成的无穷写不进响应）
func echo[Req any](t *testing.T, registry *operation.Registry, id string) {
	t.Helper()
	if err := operation.Add(registry, operation.Contract{ID: id}, operation.HandlerFunc[Req, struct{}](func(*invocation.Context, Req) primitives.Result[struct{}] {
		return primitives.Ok(struct{}{})
	}), nil, ""); err != nil {
		t.Fatal(err)
	}
}

// TestPayloadReasonsFollowTheVectors 回放 payload-reasons.json（RFC-0011 规则 2，ADR-0236）：reason 为 null 的载荷解码成功，
// 其余为 KERNEL_VALIDATION_001 + kernel.operation.invalid_payload，reason 逐字相等（语法错误一律 $）
func TestPayloadReasonsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.PayloadReasons()
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{"counts", "order", "profile"} {
		if vectors.Requests[request] == "" {
			t.Fatalf("the vector does not describe the request type %s", request)
		}
	}
	registry := operation.NewRegistry()
	echo[counts](t, registry, "counts.post")
	echo[order](t, registry, "order.post")
	echo[profile](t, registry, "profile.post")
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Cases {
		outcome := invoker.Invoke(invocation.Background(), operation.Request{ID: c.Request + ".post", Payload: []byte(c.Payload)}).Outcome
		if c.Reason == nil {
			if !outcome.OK() {
				t.Errorf("%s: %q must decode, got %v", c.Name, c.Payload, outcome.Err())
			}
			continue
		}
		failure := outcome.Err()
		if failure == nil || failure.Code != operation.ErrorCodeInvalidPayload || failure.MessageKey != operation.MessageInvalidPayload || failure.Details["reason"] != *c.Reason || failure.Details["id"] != c.Request+".post" {
			t.Errorf("%s: %q gives %v, want reason %q", c.Name, c.Payload, failure, *c.Reason)
		}
	}
}
