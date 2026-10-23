package caching_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/caching"
	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// clock 是可拨动的时钟。
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newStore(t *testing.T, options ...caching.MemoryOption) (*caching.MemoryStore, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
	return caching.NewMemoryStore(append([]caching.MemoryOption{caching.WithClock(c.Now)}, options...)...), c
}

func TestMemoryStoreExpiresAndSlides(t *testing.T) {
	store, clock := newStore(t, caching.WithDefaultTimeToLive(time.Minute))
	ctx := context.Background()
	if err := store.Set(ctx, "a", []byte("1"), caching.Options{TimeToLive: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "s", []byte("2"), caching.Options{Sliding: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "d", []byte("3"), caching.Options{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(8 * time.Second)
	if value, found, _ := store.Get(ctx, "s"); !found || string(value) != "2" {
		t.Error("sliding entry is readable and extended")
	}
	clock.Advance(8 * time.Second)
	if _, found, _ := store.Get(ctx, "a"); found {
		t.Error("absolute entry expired")
	}
	if _, found, _ := store.Get(ctx, "s"); !found {
		t.Error("sliding entry was extended on read")
	}
	if _, found, _ := store.Get(ctx, "d"); !found {
		t.Error("default ttl is one minute")
	}
	clock.Advance(time.Minute)
	if _, found, _ := store.Get(ctx, "s"); found {
		t.Error("sliding entry expires without reads")
	}
	if removed, _ := store.Remove(ctx, "d"); removed {
		t.Error("removing an expired entry reports false")
	}
}

func TestMemoryStoreAtomicOperations(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	var wins int32
	var mu sync.Mutex
	var group sync.WaitGroup
	for i := range 32 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			added, err := store.TryAdd(ctx, "lock", []byte{byte(i)}, caching.Options{TimeToLive: time.Minute})
			if err != nil {
				t.Error(err)
			}
			if added {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	group.Wait()
	if wins != 1 {
		t.Errorf("%d winners, want 1", wins)
	}
	value, _, _ := store.Get(ctx, "lock")
	if removed, _ := store.TryRemove(ctx, "lock", []byte("other")); removed {
		t.Error("try-remove requires the matching value")
	}
	if removed, _ := store.TryRemove(ctx, "lock", value); !removed {
		t.Error("try-remove with the matching value")
	}
	if added, _ := store.TryAdd(ctx, "lock", []byte("x"), caching.Options{}); !added {
		t.Error("the key is free again")
	}
	if err := store.Set(ctx, "lock", []byte("y"), caching.Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.Get(ctx, "lock"); !bytes.Equal(got, []byte("y")) {
		t.Error("set overwrites")
	}
	got, _, _ := store.Get(ctx, "lock")
	got[0] = 'z'
	if again, _, _ := store.Get(ctx, "lock"); !bytes.Equal(again, []byte("y")) {
		t.Error("returned bytes are copies")
	}
}

func TestMemoryStoreValidatesAndBoundsCapacity(t *testing.T) {
	store, clock := newStore(t, caching.WithCapacity(2))
	ctx := context.Background()
	if err := store.Set(ctx, "has space", []byte("x"), caching.Options{}); err == nil || err.(interface{ Error() string }).Error() == "" {
		t.Error("invalid key")
	}
	if err := store.Set(ctx, "k", []byte("x"), caching.Options{TimeToLive: time.Second, Sliding: time.Second}); err == nil {
		t.Error("exclusive expirations")
	}
	_ = store.Set(ctx, "a", []byte("1"), caching.Options{TimeToLive: time.Second})
	_ = store.Set(ctx, "b", []byte("2"), caching.Options{TimeToLive: time.Minute})
	if err := store.Set(ctx, "c", []byte("3"), caching.Options{}); err == nil {
		t.Error("the store is full")
	}
	clock.Advance(2 * time.Second)
	if err := store.Set(ctx, "c", []byte("3"), caching.Options{}); err != nil {
		t.Errorf("expired entries are evicted to make room: %v", err)
	}
	if store.Len() != 2 {
		t.Errorf("len %d", store.Len())
	}
}

func TestOperationsReplayTheVectors(t *testing.T) {
	vectors, err := conformance.CachingDistributed()
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := caching.RegisterOperations(registry, caching.NewMemoryStore()); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range registry.List() {
		if !contains(vectors.Operations, descriptor.Contract.ID) || descriptor.Contract.Stability.String() != "internal" || len(descriptor.Contract.Expose) != 0 {
			t.Errorf("operation %+v must be internal and listed", descriptor.Contract)
		}
	}
	if len(registry.List()) != len(vectors.Operations) {
		t.Errorf("%d operations registered, want %d", len(registry.List()), len(vectors.Operations))
	}
	invoker, err := operation.NewInvoker(registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, sample := range vectors.Samples {
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: sample.Operation, Payload: sample.Request})
		if !response.Outcome.OK() {
			t.Fatalf("sample %d %s: %v", index, sample.Operation, response.Outcome)
		}
		if canonical(t, response.Payload) != canonical(t, sample.Response) {
			t.Errorf("sample %d %s: got %s, want %s", index, sample.Operation, response.Payload, sample.Response)
		}
	}
	for _, key := range vectors.InvalidKeys {
		payload, _ := json.Marshal(map[string]any{"key": key, "value": ""})
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: caching.OperationSet, Payload: payload})
		if response.Outcome.OK() || response.Outcome.Err().Code != vectors.Errors["invalidKey"] {
			t.Errorf("key %q: %v", key, response.Outcome)
		}
	}
	for _, options := range vectors.InvalidOptions {
		var fields map[string]any
		_ = json.Unmarshal(options, &fields)
		fields["key"] = "k"
		fields["value"] = ""
		payload, _ := json.Marshal(fields)
		response := invoker.Invoke(invocation.Background(), operation.Request{ID: caching.OperationTryAdd, Payload: payload})
		if response.Outcome.OK() || response.Outcome.Err().Code != vectors.Errors["invalidOptions"] {
			t.Errorf("options %s: %v", options, response.Outcome)
		}
	}
	bad := invoker.Invoke(invocation.Background(), operation.Request{ID: caching.OperationSet, Payload: []byte(`{"key":"k","value":"%%%"}`)})
	if bad.Outcome.OK() || bad.Outcome.Err().Code != operation.ErrorCodeInvalidPayload {
		t.Errorf("bad base64: %v", bad.Outcome)
	}
}

func canonical(t *testing.T, data []byte) string {
	t.Helper()
	if len(bytes.TrimSpace(data)) == 0 {
		return "{}"
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("not JSON: %s", data)
	}
	encoded, _ := json.Marshal(value)
	return strings.TrimSpace(string(encoded))
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestMemoryStoreBatchOperations(t *testing.T) {
	store, clock := newStore(t, caching.WithCapacity(3))
	ctx := context.Background()

	// 空集合不触后端、返回空结果
	if lookups, err := store.GetMany(ctx, nil); err != nil || len(lookups) != 0 {
		t.Fatalf("empty get-many: %v %v", lookups, err)
	}
	if err := store.SetMany(ctx, nil); err != nil {
		t.Fatalf("empty set-many: %v", err)
	}
	if removed, err := store.RemoveMany(ctx, []string{}); err != nil || len(removed) != 0 {
		t.Fatalf("empty remove-many: %v %v", removed, err)
	}

	// 同一键出现多次后者覆盖；各项带各自的过期
	err := store.SetMany(ctx, []caching.Entry{
		{Key: "a", Value: []byte("1"), Options: caching.Options{TimeToLive: time.Second}},
		{Key: "b", Value: []byte("2"), Options: caching.Options{Sliding: 2 * time.Second}},
		{Key: "a", Value: []byte("3"), Options: caching.Options{TimeToLive: time.Second}},
	})
	if err != nil {
		t.Fatalf("set-many: %v", err)
	}
	lookups, err := store.GetMany(ctx, []string{"a", "missing", "b", "a"})
	if err != nil {
		t.Fatalf("get-many: %v", err)
	}
	if string(lookups[0].Value) != "3" || lookups[1].Found || string(lookups[2].Value) != "2" || string(lookups[3].Value) != "3" {
		t.Fatalf("get-many results: %+v", lookups)
	}

	// 滑动过期在批量读取时延长，绝对过期不延长
	clock.Advance(1500 * time.Millisecond)
	lookups, _ = store.GetMany(ctx, []string{"a", "b"})
	if lookups[0].Found || !lookups[1].Found {
		t.Fatalf("after 1.5s: %+v", lookups)
	}
	clock.Advance(1500 * time.Millisecond)
	if value, found, _ := store.Get(ctx, "b"); !found || string(value) != "2" {
		t.Fatalf("sliding entry should have been extended by get-many")
	}

	// 容量按去重后的新键数整批判定：超出时一条都不写
	if err := store.SetMany(ctx, []caching.Entry{{Key: "c", Value: []byte("c")}, {Key: "d", Value: []byte("d")}, {Key: "e", Value: []byte("e")}}); err == nil || err.(*primitives.Error).Code != caching.ErrorCodeUnavailable {
		t.Fatalf("capacity: %v", err)
	}
	if _, found, _ := store.Get(ctx, "c"); found {
		t.Fatalf("a rejected batch must not write partially")
	}
	if err := store.SetMany(ctx, []caching.Entry{{Key: "c", Value: []byte("c")}, {Key: "c", Value: []byte("c2")}, {Key: "b", Value: []byte("b2")}}); err != nil {
		t.Fatalf("duplicates and overwrites do not take new slots: %v", err)
	}

	// 逐键删除结果与入参等长，同一键第二次为假
	removed, err := store.RemoveMany(ctx, []string{"c", "c", "b", "missing"})
	if err != nil {
		t.Fatalf("remove-many: %v", err)
	}
	if want := []bool{true, false, true, false}; !equalBools(removed, want) {
		t.Fatalf("remove-many results %v, want %v", removed, want)
	}

	// 不合法的键整批拒绝并带下标
	if _, err := store.GetMany(ctx, []string{"ok", "not ok"}); err == nil || err.(*primitives.Error).Details["index"] != 1 {
		t.Fatalf("invalid key index: %v", err)
	}
	if err := store.SetMany(ctx, []caching.Entry{{Key: "ok"}, {Key: "x", Options: caching.Options{TimeToLive: time.Second, Sliding: time.Second}}}); err == nil || err.(*primitives.Error).Details["index"] != 1 {
		t.Fatalf("invalid options index: %v", err)
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
