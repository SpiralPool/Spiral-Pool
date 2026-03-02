// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package atomicmap provides a sharded concurrent map optimized for high-throughput access.
// It minimizes lock contention by distributing keys across multiple shards.
package atomicmap

import (
	"sync"
)

const (
	// DefaultShardCount is the default number of shards.
	// 256 provides good distribution while keeping memory overhead reasonable.
	DefaultShardCount = 256
)

// ShardedMap is a concurrent map that distributes keys across multiple shards
// to minimize lock contention.
type ShardedMap[K comparable, V any] struct {
	shards    []shard[K, V]
	shardMask uint64
	hashFunc  func(K) uint64
}

type shard[K comparable, V any] struct {
	sync.RWMutex
	items map[K]V
}

// New creates a new ShardedMap with the specified shard count and hash function.
// shardCount must be a power of 2.
func New[K comparable, V any](shardCount int, hashFunc func(K) uint64) *ShardedMap[K, V] {
	if shardCount <= 0 {
		shardCount = DefaultShardCount
	}
	// Ensure power of 2
	if shardCount&(shardCount-1) != 0 {
		shardCount--
		shardCount |= shardCount >> 1
		shardCount |= shardCount >> 2
		shardCount |= shardCount >> 4
		shardCount |= shardCount >> 8
		shardCount |= shardCount >> 16
		shardCount++
	}

	sm := &ShardedMap[K, V]{
		shards:    make([]shard[K, V], shardCount),
		shardMask: uint64(shardCount - 1),
		hashFunc:  hashFunc,
	}

	for i := range sm.shards {
		sm.shards[i].items = make(map[K]V)
	}

	return sm
}

// getShard returns the shard for the given key.
func (sm *ShardedMap[K, V]) getShard(key K) *shard[K, V] {
	hash := sm.hashFunc(key)
	return &sm.shards[hash&sm.shardMask]
}

// Get retrieves a value from the map.
func (sm *ShardedMap[K, V]) Get(key K) (V, bool) {
	s := sm.getShard(key)
	s.RLock()
	val, ok := s.items[key]
	s.RUnlock()
	return val, ok
}

// Set stores a value in the map.
func (sm *ShardedMap[K, V]) Set(key K, value V) {
	s := sm.getShard(key)
	s.Lock()
	s.items[key] = value
	s.Unlock()
}

// Delete removes a key from the map.
func (sm *ShardedMap[K, V]) Delete(key K) {
	s := sm.getShard(key)
	s.Lock()
	delete(s.items, key)
	s.Unlock()
}

// GetOrSet returns the existing value for key, or sets and returns the given value if not present.
func (sm *ShardedMap[K, V]) GetOrSet(key K, value V) (actual V, loaded bool) {
	s := sm.getShard(key)
	s.Lock()
	if existing, ok := s.items[key]; ok {
		s.Unlock()
		return existing, true
	}
	s.items[key] = value
	s.Unlock()
	return value, false
}

// Update atomically updates a value using the provided function.
// If the key doesn't exist, the function receives the zero value.
func (sm *ShardedMap[K, V]) Update(key K, fn func(V, bool) V) {
	s := sm.getShard(key)
	s.Lock()
	existing, ok := s.items[key]
	s.items[key] = fn(existing, ok)
	s.Unlock()
}

// Len returns the total number of items across all shards.
func (sm *ShardedMap[K, V]) Len() int {
	count := 0
	for i := range sm.shards {
		sm.shards[i].RLock()
		count += len(sm.shards[i].items)
		sm.shards[i].RUnlock()
	}
	return count
}

// Range calls fn for each key-value pair in the map.
// If fn returns false, iteration stops.
// Note: The map may be modified during iteration; fn should not rely on consistency.
func (sm *ShardedMap[K, V]) Range(fn func(K, V) bool) {
	for i := range sm.shards {
		sm.shards[i].RLock()
		for k, v := range sm.shards[i].items {
			if !fn(k, v) {
				sm.shards[i].RUnlock()
				return
			}
		}
		sm.shards[i].RUnlock()
	}
}

// Keys returns all keys in the map.
func (sm *ShardedMap[K, V]) Keys() []K {
	keys := make([]K, 0, sm.Len())
	for i := range sm.shards {
		sm.shards[i].RLock()
		for k := range sm.shards[i].items {
			keys = append(keys, k)
		}
		sm.shards[i].RUnlock()
	}
	return keys
}

// Values returns all values in the map.
func (sm *ShardedMap[K, V]) Values() []V {
	values := make([]V, 0, sm.Len())
	for i := range sm.shards {
		sm.shards[i].RLock()
		for _, v := range sm.shards[i].items {
			values = append(values, v)
		}
		sm.shards[i].RUnlock()
	}
	return values
}

// Clear removes all items from the map.
func (sm *ShardedMap[K, V]) Clear() {
	for i := range sm.shards {
		sm.shards[i].Lock()
		sm.shards[i].items = make(map[K]V)
		sm.shards[i].Unlock()
	}
}

// UInt64Hash is a simple hash function for uint64 keys.
func UInt64Hash(key uint64) uint64 {
	// Mix bits for better distribution
	key ^= key >> 33
	key *= 0xff51afd7ed558ccd
	key ^= key >> 33
	key *= 0xc4ceb9fe1a85ec53
	key ^= key >> 33
	return key
}

// StringHash is a simple hash function for string keys.
func StringHash(key string) uint64 {
	var hash uint64 = 14695981039346656037 // FNV offset basis
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= 1099511628211 // FNV prime
	}
	return hash
}
