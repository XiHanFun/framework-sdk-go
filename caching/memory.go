package caching

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 内存存储的默认值。
const (
	// DefaultTimeToLive 是没有指定过期时的存活时间。
	DefaultTimeToLive = 5 * time.Minute
	// DefaultCapacity 是条目数上限。
	DefaultCapacity = 100_000
)

// MemoryStore 是有界的进程内 Store：互斥锁串行化，读取时判定过期并延长滑动过期，满了先清过期条目、仍满则拒绝。
// 批量方法一次加锁逐项处理。
type MemoryStore struct {
	mu         sync.Mutex
	entries    map[string]*entry
	now        func() time.Time
	defaultTTL time.Duration
	capacity   int
}

// entry 是一个条目。
type entry struct {
	value   []byte
	expires time.Time
	sliding time.Duration
}

// MemoryOption 配置 MemoryStore。
type MemoryOption func(*MemoryStore)

// WithClock 指定时钟（测试用）。
func WithClock(now func() time.Time) MemoryOption {
	return func(s *MemoryStore) { s.now = now }
}

// WithDefaultTimeToLive 指定默认存活时间。
func WithDefaultTimeToLive(ttl time.Duration) MemoryOption {
	return func(s *MemoryStore) {
		if ttl > 0 {
			s.defaultTTL = ttl
		}
	}
}

// WithCapacity 指定条目数上限。
func WithCapacity(capacity int) MemoryOption {
	return func(s *MemoryStore) {
		if capacity > 0 {
			s.capacity = capacity
		}
	}
}

// NewMemoryStore 创建内存存储。
func NewMemoryStore(options ...MemoryOption) *MemoryStore {
	s := &MemoryStore{entries: map[string]*entry{}, now: time.Now, defaultTTL: DefaultTimeToLive, capacity: DefaultCapacity}
	for _, option := range options {
		option(s)
	}
	return s
}

// Len 返回当前条目数（含尚未清理的过期条目）。
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Get 实现 Store。
func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	if err := ValidateKey(key); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.get(key, s.now())
	return value, found, nil
}

// Set 实现 Store。
func (s *MemoryStore) Set(_ context.Context, key string, value []byte, options Options) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := options.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := s.reserve(key, now); err != nil {
		return err
	}
	s.entries[key] = s.newEntry(value, options, now)
	return nil
}

// TryAdd 实现 Store。
func (s *MemoryStore) TryAdd(_ context.Context, key string, value []byte, options Options) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	if err := options.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.live(key, now) != nil {
		return false, nil
	}
	if err := s.reserve(key, now); err != nil {
		return false, err
	}
	s.entries[key] = s.newEntry(value, options, now)
	return true, nil
}

// Remove 实现 Store。
func (s *MemoryStore) Remove(_ context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(key, s.now()), nil
}

// TryRemove 实现 Store。
func (s *MemoryStore) TryRemove(_ context.Context, key string, expected []byte) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.live(key, s.now())
	if e == nil || !bytes.Equal(e.value, expected) {
		return false, nil
	}
	delete(s.entries, key)
	return true, nil
}

// GetMany 实现 Store：全部键先校验，再一次加锁逐键读取。
func (s *MemoryStore) GetMany(_ context.Context, keys []string) ([]Lookup, error) {
	if err := ValidateKeys(keys); err != nil {
		return nil, err
	}
	results := make([]Lookup, len(keys))
	if len(keys) == 0 {
		return results, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for index, key := range keys {
		value, found := s.get(key, now)
		results[index] = Lookup{Value: value, Found: found}
	}
	return results, nil
}

// SetMany 实现 Store：全部项先校验，锁内先按去重后的新键数判定容量（不够先清过期、仍不够整批拒绝），再逐项写入。
func (s *MemoryStore) SetMany(_ context.Context, entries []Entry) error {
	for index, e := range entries {
		if err := ValidateKey(e.Key); err != nil {
			return withIndex(err, index)
		}
		if err := e.Options.Validate(); err != nil {
			return withIndex(err, index)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// 容量按"写入后的条目数"判定：覆盖已有键不占新位置，同一键出现多次只算一个
	additions := map[string]struct{}{}
	for _, e := range entries {
		if _, exists := s.entries[e.Key]; !exists {
			additions[e.Key] = struct{}{}
		}
	}
	if len(s.entries)+len(additions) > s.capacity {
		s.purgeExpired(now)
		for key := range additions {
			if _, exists := s.entries[key]; exists {
				delete(additions, key)
			}
		}
		if len(s.entries)+len(additions) > s.capacity {
			return primitives.Unavailable(ErrorCodeUnavailable, MessageCapacityReached, map[string]any{"capacity": s.capacity})
		}
	}
	for _, e := range entries {
		s.entries[e.Key] = s.newEntry(e.Value, e.Options, now)
	}
	return nil
}

// RemoveMany 实现 Store：全部键先校验，再一次加锁逐键删除；同一键第二次为假。
func (s *MemoryStore) RemoveMany(_ context.Context, keys []string) ([]bool, error) {
	if err := ValidateKeys(keys); err != nil {
		return nil, err
	}
	results := make([]bool, len(keys))
	if len(keys) == 0 {
		return results, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for index, key := range keys {
		results[index] = s.remove(key, now)
	}
	return results, nil
}

// get 读取未过期的条目并延长滑动过期；返回值的副本。调用方持锁。
func (s *MemoryStore) get(key string, now time.Time) ([]byte, bool) {
	e := s.live(key, now)
	if e == nil {
		return nil, false
	}
	// 滑动过期在读取时延长
	if e.sliding > 0 {
		e.expires = now.Add(e.sliding)
	}
	return bytes.Clone(e.value), true
}

// remove 删除条目并报告删除前是否存在（已过期视为不存在）。调用方持锁。
func (s *MemoryStore) remove(key string, now time.Time) bool {
	existed := s.live(key, now) != nil
	delete(s.entries, key)
	return existed
}

// live 返回未过期的条目；过期的顺手删掉。调用方持锁。
func (s *MemoryStore) live(key string, now time.Time) *entry {
	e, ok := s.entries[key]
	if !ok {
		return nil
	}
	if !now.Before(e.expires) {
		delete(s.entries, key)
		return nil
	}
	return e
}

// reserve 为新键腾位置：满了先清过期条目，仍满则拒绝。调用方持锁。
func (s *MemoryStore) reserve(key string, now time.Time) error {
	if _, exists := s.entries[key]; exists || len(s.entries) < s.capacity {
		return nil
	}
	s.purgeExpired(now)
	if len(s.entries) >= s.capacity {
		return primitives.Unavailable(ErrorCodeUnavailable, MessageCapacityReached, map[string]any{"capacity": s.capacity})
	}
	return nil
}

// purgeExpired 清掉全部过期条目。调用方持锁。
func (s *MemoryStore) purgeExpired(now time.Time) {
	for candidate, e := range s.entries {
		if !now.Before(e.expires) {
			delete(s.entries, candidate)
		}
	}
}

// newEntry 按选项创建条目：滑动过期从现在起算，没有选项用默认存活时间。
func (s *MemoryStore) newEntry(value []byte, options Options, now time.Time) *entry {
	e := &entry{value: bytes.Clone(value)}
	switch {
	case options.Sliding > 0:
		e.sliding = options.Sliding
		e.expires = now.Add(options.Sliding)
	case options.TimeToLive > 0:
		e.expires = now.Add(options.TimeToLive)
	default:
		e.expires = now.Add(s.defaultTTL)
	}
	return e
}
