package primitives_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

func TestErrorCodesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.ErrorCodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range vectors.Valid {
		if !primitives.IsErrorCode(code) {
			t.Errorf("%q must be valid", code)
		}
	}
	for _, code := range vectors.Invalid {
		if primitives.IsErrorCode(code) {
			t.Errorf("%q must be invalid", code)
		}
	}
	if primitives.ErrorCodeKind("PERSISTENCE_CONFLICT_001") != "CONFLICT" || primitives.ErrorCodeDomain("PERSISTENCE_CONFLICT_001") != "PERSISTENCE" {
		t.Error("kind and domain")
	}
}

func TestBaggageKeysFollowTheVectors(t *testing.T) {
	vectors, err := conformance.BaggageKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range vectors.WellFormed {
		if !primitives.IsBaggageKeyWellFormed(key) {
			t.Errorf("%q must be well-formed", key)
		}
	}
	for _, key := range vectors.IllFormed {
		if primitives.IsBaggageKeyWellFormed(key) {
			t.Errorf("%q must be ill-formed", key)
		}
	}
	registered := primitives.RegisteredBaggageKeys()
	if len(registered) != len(vectors.Registered) {
		t.Fatalf("registered keys = %v, want %v", registered, vectors.Registered)
	}
	for index, key := range vectors.Registered {
		if registered[index] != key || !primitives.IsBaggageKeyRegistered(key) {
			t.Errorf("registered[%d] = %q, want %q", index, registered[index], key)
		}
	}
}

func TestLifecycleTransitionsFollowTheVectors(t *testing.T) {
	vectors, err := conformance.LifecycleTransitions()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[[2]string]bool{}
	for _, pair := range vectors.Allowed {
		allowed[pair] = true
		from, ok := primitives.ParseLifecycleState(pair[0])
		if !ok {
			t.Fatalf("unknown state %q", pair[0])
		}
		to, ok := primitives.ParseLifecycleState(pair[1])
		if !ok {
			t.Fatalf("unknown state %q", pair[1])
		}
		if !from.CanTransitionTo(to) {
			t.Errorf("%s -> %s must be allowed", from, to)
		}
	}
	for _, from := range primitives.LifecycleStates() {
		for _, to := range primitives.LifecycleStates() {
			if from.CanTransitionTo(to) && !allowed[[2]string{from.String(), to.String()}] {
				t.Errorf("%s -> %s is allowed by the SDK but absent from the vector", from, to)
			}
		}
	}
	if !primitives.Ready.IsServing() || !primitives.Degraded.IsServing() || primitives.Started.IsServing() || !primitives.Stopped.IsTerminal() {
		t.Error("serving and terminal states")
	}
	if int(primitives.Failed) != 8 || int(primitives.Declared) != 0 {
		t.Error("state numbers must match the protocol enum")
	}
}

func TestPipelineStagesAreOrdered(t *testing.T) {
	stages := primitives.PipelineStages()
	names := []string{"ingress", "observe", "resilience", "authenticate", "tenant", "authorize", "validate", "idempotency", "transaction", "handle", "egress"}
	if len(stages) != len(names) {
		t.Fatalf("%d stages", len(stages))
	}
	for index, stage := range stages {
		if stage.ManifestName() != names[index] {
			t.Errorf("stage %d = %s, want %s", index, stage, names[index])
		}
		parsed, ok := primitives.ParsePipelineStage(names[index])
		if !ok || parsed != stage {
			t.Errorf("parse %s", names[index])
		}
	}
	if _, ok := primitives.ParsePipelineStage("Handle"); ok {
		t.Error("stage names are lowercase")
	}
}

func TestErrorFactoriesCheckTheKind(t *testing.T) {
	err := primitives.Validation("ORDERS_VALIDATION_001", "orders.quantity.invalid", map[string]any{"min": 1})
	if err.Kind() != "VALIDATION" || err.Domain() != "ORDERS" {
		t.Error("kind and domain")
	}
	if err.Message() != "orders.quantity.invalid (min=1)" {
		t.Errorf("message = %q", err.Message())
	}
	defer func() {
		var framework *primitives.FrameworkError
		if recovered := recover(); recovered == nil || !errors.As(recovered.(error), &framework) || framework.Code != primitives.ErrorCodeInvalidArgument {
			t.Errorf("a mismatched kind must panic with PRIMITIVES_VALIDATION_001, got %v", recovered)
		}
	}()
	primitives.NotFound("ORDERS_VALIDATION_001", "orders.missing", nil)
}

func TestErrorMessagesResolveThroughTheDefaultCatalog(t *testing.T) {
	primitives.SetDefaultMessages(primitives.Catalog{
		"en-US": {"orders.missing": "Order {id} was not found."},
		"zh-CN": {"orders.missing": "订单 {id} 不存在。"},
	}, "zh-CN")
	t.Cleanup(func() { primitives.SetDefaultMessages(nil, "en-US") })
	err := primitives.NotFound("ORDERS_NOTFOUND_001", "orders.missing", map[string]any{"id": 42})
	if err.Message() != "订单 42 不存在。" {
		t.Errorf("message = %q", err.Message())
	}
	if err.WithText("literal").Message() != "literal" {
		t.Error("text wins")
	}
	if primitives.FormatMessage("{0} of {count:%03d}", map[string]any{"0": "a", "count": 7}) != "a of 007" {
		t.Error("format")
	}
	if primitives.FormatMessage("{missing}", nil) != "{missing}" {
		t.Error("missing placeholders stay")
	}
}

func TestErrorWireEncoding(t *testing.T) {
	inner := primitives.Literal("KERNEL_UNEXPECTED_001", "boom", nil, nil)
	err := primitives.Unexpected("ORDERS_UNEXPECTED_001", "orders.failed", inner, map[string]any{"id": "o1"})
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	want := `{"code":"ORDERS_UNEXPECTED_001","message":"orders.failed (id=o1)","messageKey":"orders.failed","details":{"id":"o1"},"inner":{"code":"KERNEL_UNEXPECTED_001","message":"boom"}}`
	if string(data) != want {
		t.Errorf("encoded %s", data)
	}
	var decoded primitives.Error
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Code != "ORDERS_UNEXPECTED_001" || decoded.Text != "orders.failed (id=o1)" || decoded.MessageKey != "orders.failed" || decoded.Inner == nil || decoded.Inner.Code != "KERNEL_UNEXPECTED_001" {
		t.Errorf("decoded %+v", decoded)
	}
	if json.Unmarshal([]byte(`{"code":"bad"}`), &decoded) == nil {
		t.Error("an invalid code on the wire must be rejected")
	}
}

func TestResultWireEncoding(t *testing.T) {
	ok := primitives.Ok(map[string]any{"id": "o1"})
	data, _ := json.Marshal(ok)
	if string(data) != `{"ok":true,"value":{"id":"o1"}}` {
		t.Errorf("encoded %s", data)
	}
	failed := primitives.Fail[map[string]any](primitives.Literal("ORDERS_CONFLICT_001", "stale", nil, nil))
	data, _ = json.Marshal(failed)
	if string(data) != `{"ok":false,"error":{"code":"ORDERS_CONFLICT_001","message":"stale"}}` {
		t.Errorf("encoded %s", data)
	}
	var decoded primitives.Result[map[string]any]
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.OK() || decoded.Err().Code != "ORDERS_CONFLICT_001" {
		t.Errorf("decoded %v (%v)", decoded, err)
	}
	var uninitialized primitives.Result[int]
	if uninitialized.OK() || uninitialized.Err().Code != primitives.ErrorCodeUninitializedResult {
		t.Error("the zero result is an uninitialized failure")
	}
	doubled := primitives.Map(primitives.Ok(21), func(value int) int { return value * 2 })
	if value, _ := doubled.Value(); value != 42 {
		t.Error("map")
	}
	if primitives.Bind(failed, func(map[string]any) primitives.Result[int] { return primitives.Ok(1) }).OK() {
		t.Error("bind propagates failures")
	}
}

func TestMaybeWireEncoding(t *testing.T) {
	data, _ := json.Marshal(primitives.Some("x"))
	if string(data) != `"x"` {
		t.Errorf("encoded %s", data)
	}
	data, _ = json.Marshal(primitives.None[string]())
	if string(data) != "null" {
		t.Errorf("encoded %s", data)
	}
	var decoded primitives.Maybe[int]
	if err := json.Unmarshal([]byte("null"), &decoded); err != nil || decoded.HasValue() {
		t.Error("null decodes to none")
	}
	if err := json.Unmarshal([]byte("5"), &decoded); err != nil || decoded.Or(0) != 5 {
		t.Error("value decodes to some")
	}
	if primitives.None[int]().ToResult(primitives.Literal("ORDERS_NOTFOUND_001", "missing", nil, nil)).OK() {
		t.Error("none to result is a failure")
	}
}

type orderTag struct{}

func TestTypedIDEncodesAsItsValue(t *testing.T) {
	id := primitives.NewID[orderTag]("o1")
	data, _ := json.Marshal(id)
	if string(data) != `"o1"` {
		t.Errorf("encoded %s", data)
	}
	var decoded primitives.ID[orderTag, string]
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Value != "o1" {
		t.Error("decode")
	}
}

func TestStabilityNames(t *testing.T) {
	for _, name := range []string{"stable", "preview", "experimental", "internal", "deprecated"} {
		stability, ok := primitives.ParseStability(name)
		if !ok || stability.String() != name {
			t.Errorf("stability %s", name)
		}
	}
	data, _ := json.Marshal(primitives.Experimental)
	if string(data) != `"experimental"` {
		t.Errorf("encoded %s", data)
	}
}
