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
