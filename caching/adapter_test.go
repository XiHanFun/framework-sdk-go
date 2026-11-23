package caching_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/caching"
	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// laxStore 是什么都不校验的 Store：记下被调用的方法，fail 不为空时每个方法都返回它。操作集自己该做的校验不能指望 Store 实现
type laxStore struct {
	calls []string
	fail  error
}

// record 记一次调用并返回要报的错误
func (s *laxStore) record(method string) error {
	s.calls = append(s.calls, method)
	return s.fail
}

func (s *laxStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, s.record("Get")
}

func (s *laxStore) Set(context.Context, string, []byte, caching.Options) error {
	return s.record("Set")
}

func (s *laxStore) TryAdd(context.Context, string, []byte, caching.Options) (bool, error) {
	return true, s.record("TryAdd")
}

func (s *laxStore) Remove(context.Context, string) (bool, error) {
	return true, s.record("Remove")
}

func (s *laxStore) TryRemove(context.Context, string, []byte) (bool, error) {
	return true, s.record("TryRemove")
}

func (s *laxStore) TryRefresh(context.Context, string, []byte, time.Duration) (bool, error) {
	return true, s.record("TryRefresh")
}

func (s *laxStore) GetMany(_ context.Context, keys []string) ([]caching.Lookup, error) {
	return make([]caching.Lookup, len(keys)), s.record("GetMany")
}

func (s *laxStore) SetMany(context.Context, []caching.Entry) error {
	return s.record("SetMany")
}

func (s *laxStore) RemoveMany(_ context.Context, keys []string) ([]bool, error) {
	return make([]bool, len(keys)), s.record("RemoveMany")
}

// adapt 把 store 暴露成操作集并返回调用器
func adapt(t *testing.T, store caching.Store) *operation.Invoker {
	t.Helper()
	registry := operation.NewRegistry()
	if err := caching.RegisterOperations(registry, store); err != nil {
		t.Fatal(err)
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

// requestsFor 给出九个操作各自带这个键的请求；批量操作把它放在下标 1，前面是一个合法的键
func requestsFor(t *testing.T, key string) map[string][]byte {
	t.Helper()
	encode := func(value any) []byte {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	return map[string][]byte{
		caching.OperationGet:        encode(map[string]any{"key": key}),
		caching.OperationSet:        encode(map[string]any{"key": key, "value": ""}),
		caching.OperationTryAdd:     encode(map[string]any{"key": key, "value": ""}),
		caching.OperationRemove:     encode(map[string]any{"key": key}),
		caching.OperationTryRemove:  encode(map[string]any{"key": key, "expected": ""}),
		caching.OperationTryRefresh: encode(map[string]any{"key": key, "expected": "", "ttlMs": 100}),
		caching.OperationGetMany:    encode(map[string]any{"keys": []string{"ok", key}}),
		caching.OperationSetMany:    encode(map[string]any{"entries": []map[string]any{{"key": "ok", "value": ""}, {"key": key, "value": ""}}}),
		caching.OperationRemoveMany: encode(map[string]any{"keys": []string{"ok", key}}),
	}
}

// TestEveryOperationValidatesTheKeyBeforeTheStore 核对九个操作都在操作集里先校验键（provider/caching-distributed.md §2、§4），不指望
// Store 实现：向量里的每个非法键都以 CACHING_VALIDATION_001 + caching.remote.key_invalid 结束，批量操作带出错项的下标，Store 不被调用
func TestEveryOperationValidatesTheKeyBeforeTheStore(t *testing.T) {
	vectors, err := conformance.CachingDistributed()
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors.InvalidKeys) == 0 {
		t.Fatal("no invalid keys")
	}
	store := &laxStore{}
	invoker := adapt(t, store)
	batch := map[string]bool{caching.OperationGetMany: true, caching.OperationSetMany: true, caching.OperationRemoveMany: true}
	for _, key := range vectors.InvalidKeys {
		requests := requestsFor(t, key)
		if len(requests) != len(caching.Operations) {
			t.Fatalf("%d requests for %d operations", len(requests), len(caching.Operations))
		}
		for _, id := range caching.Operations {
			failure := invoker.Invoke(invocation.Background(), operation.Request{ID: id, Payload: requests[id]}).Outcome.Err()
			if failure == nil || failure.Code != vectors.Errors["invalidKey"] || failure.MessageKey != caching.MessageInvalidKey || failure.Details["key"] != key {
				t.Errorf("%s with the key %.40q: got %v", id, key, failure)
				continue
			}
			if index, has := failure.Details["index"]; has != batch[id] || (has && index != 1) {
				t.Errorf("%s with the key %.40q: index %v (present %v)", id, key, index, has)
			}
		}
	}
	if len(store.calls) != 0 {
		t.Errorf("the store must not be reached with an invalid key, got %v", store.calls)
	}

	// 合法的键照常到达 Store，每个操作一次
	for _, id := range caching.Operations {
		if response := invoker.Invoke(invocation.Background(), operation.Request{ID: id, Payload: requestsFor(t, "orders:1")[id]}); !response.Outcome.OK() {
			t.Errorf("%s with a valid key: %v", id, response.Outcome)
		}
	}
	if len(store.calls) != len(caching.Operations) {
		t.Errorf("each operation reaches the store once, got %v", store.calls)
	}

	// 键先于期望值校验：键与 base64 都不合法时报键
	both := invoker.Invoke(invocation.Background(), operation.Request{ID: caching.OperationTryRemove, Payload: []byte(`{"key":"has space","expected":"%%%"}`)})
	if failure := both.Outcome.Err(); failure == nil || failure.Code != caching.ErrorCodeInvalidKey {
		t.Errorf("try-remove validates the key before the expected value, got %v", failure)
	}
}

// TestStoreErrorsAreFoundThroughWrapping 核对 Store 返回的错误：包了一层的 *primitives.Error 原样交给调用方，不当成后端不可用；
// 别的错误才是 CACHING_UNAVAILABLE_001
func TestStoreErrorsAreFoundThroughWrapping(t *testing.T) {
	conflict := primitives.Conflict("ORDERS_CONFLICT_001", "orders.conflict", map[string]any{"id": "o1"})
	cases := []struct {
		name string
		fail error
		code string
	}{
		{"a primitives error", conflict, "ORDERS_CONFLICT_001"},
		{"a wrapped primitives error", fmt.Errorf("redis: %w", conflict), "ORDERS_CONFLICT_001"},
		{"a doubly wrapped primitives error", fmt.Errorf("store: %w", fmt.Errorf("redis: %w", conflict)), "ORDERS_CONFLICT_001"},
		{"another error", errors.New("connection refused"), caching.ErrorCodeUnavailable},
	}
	for _, c := range cases {
		store := &laxStore{fail: c.fail}
		invoker := adapt(t, store)
		for _, id := range caching.Operations {
			failure := invoker.Invoke(invocation.Background(), operation.Request{ID: id, Payload: requestsFor(t, "orders:1")[id]}).Outcome.Err()
			if failure == nil || failure.Code != c.code {
				t.Errorf("%s, %s: got %v, want %s", c.name, id, failure, c.code)
				continue
			}
			if c.code == "ORDERS_CONFLICT_001" && failure != conflict {
				t.Errorf("%s, %s: the store's error must be passed through as it is", c.name, id)
			}
			if c.code == caching.ErrorCodeUnavailable && (failure.MessageKey != caching.MessageUnavailable || failure.Details["reason"] != "connection refused") {
				t.Errorf("%s, %s: %v %v", c.name, id, failure.MessageKey, failure.Details)
			}
		}
	}
}

// TestKeysRejectTheSpecWhitespace 核对键里的空白按 RFC-0001「空白字符」的 25 个码位（rules.IsWhitespace），控制字符另算；
// 零宽空格与 U+180E 不是空白也不是控制字符，照常接受
func TestKeysRejectTheSpecWhitespace(t *testing.T) {
	whitespace := []rune{
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x20, 0x85, 0xA0, 0x1680,
		0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200A,
		0x2028, 0x2029, 0x202F, 0x205F, 0x3000,
	}
	if len(whitespace) != 25 {
		t.Fatalf("the table has %d code points, want 25", len(whitespace))
	}
	for _, r := range whitespace {
		if key := "a" + string(r) + "b"; caching.IsKey(key) || caching.ValidateKey(key) == nil {
			t.Errorf("a key with the whitespace U+%04X must be rejected", r)
		}
	}
	for _, r := range []rune{0x00, 0x01, 0x1C, 0x1F, 0x7F, 0x80, 0x9F} {
		if caching.IsKey("a" + string(r) + "b") {
			t.Errorf("a key with the control character U+%04X must be rejected", r)
		}
	}
	for _, key := range []string{"orders:1", "a​b", "a᠎b", "订单:1", "a­b"} {
		if !caching.IsKey(key) {
			t.Errorf("the key %q must be accepted", key)
		}
	}
}
