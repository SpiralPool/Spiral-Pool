// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package atomicmap provides tests for the sharded concurrent map.
package atomicmap

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// =============================================================================
// UNIT TESTS
// =============================================================================

func TestNew(t *testing.T) {
	sm := New[string, int](16, StringHash)

	if sm == nil {
		t.Fatal("New returned nil")
	}

	if len(sm.shards) != 16 {
		t.Errorf("Expected 16 shards, got %d", len(sm.shards))
	}
}

func TestNew_DefaultShardCount(t *testing.T) {
	sm := New[string, int](0, StringHash)

	if len(sm.shards) != DefaultShardCount {
		t.Errorf("Expected %d shards for zero count, got %d", DefaultShardCount, len(sm.shards))
	}
}

func TestNew_NonPowerOfTwo(t *testing.T) {
	// 17 should round up to 32
	sm := New[string, int](17, StringHash)

	if len(sm.shards) != 32 {
		t.Errorf("Expected 32 shards (next power of 2 after 17), got %d", len(sm.shards))
	}
}

func TestGet_NotFound(t *testing.T) {
	sm := New[string, int](16, StringHash)

	val, ok := sm.Get("nonexistent")
	if ok {
		t.Error("Expected ok=false for nonexistent key")
	}
	if val != 0 {
		t.Errorf("Expected zero value, got %d", val)
	}
}

func TestSetAndGet(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("key1", 100)
	sm.Set("key2", 200)

	val1, ok1 := sm.Get("key1")
	if !ok1 || val1 != 100 {
		t.Errorf("Get(key1) = %d, %v; want 100, true", val1, ok1)
	}

	val2, ok2 := sm.Get("key2")
	if !ok2 || val2 != 200 {
		t.Errorf("Get(key2) = %d, %v; want 200, true", val2, ok2)
	}
}

func TestSet_Overwrite(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("key", 100)
	sm.Set("key", 200)

	val, _ := sm.Get("key")
	if val != 200 {
		t.Errorf("Expected overwritten value 200, got %d", val)
	}
}

func TestDelete(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("key", 100)
	sm.Delete("key")

	_, ok := sm.Get("key")
	if ok {
		t.Error("Key should be deleted")
	}
}

func TestDelete_NonExistent(t *testing.T) {
	sm := New[string, int](16, StringHash)

	// Should not panic
	sm.Delete("nonexistent")
}

func TestGetOrSet_Get(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("key", 100)

	val, loaded := sm.GetOrSet("key", 200)
	if !loaded {
		t.Error("Expected loaded=true for existing key")
	}
	if val != 100 {
		t.Errorf("Expected existing value 100, got %d", val)
	}
}

func TestGetOrSet_Set(t *testing.T) {
	sm := New[string, int](16, StringHash)

	val, loaded := sm.GetOrSet("key", 100)
	if loaded {
		t.Error("Expected loaded=false for new key")
	}
	if val != 100 {
		t.Errorf("Expected set value 100, got %d", val)
	}

	// Verify it was stored
	stored, _ := sm.Get("key")
	if stored != 100 {
		t.Errorf("Value was not stored correctly, got %d", stored)
	}
}

func TestUpdate(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("key", 100)

	// Increment
	sm.Update("key", func(v int, ok bool) int {
		if !ok {
			return 1
		}
		return v + 1
	})

	val, _ := sm.Get("key")
	if val != 101 {
		t.Errorf("Expected 101 after update, got %d", val)
	}
}

func TestUpdate_NewKey(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Update("newkey", func(v int, ok bool) int {
		if !ok {
			return 999
		}
		return v + 1
	})

	val, ok := sm.Get("newkey")
	if !ok || val != 999 {
		t.Errorf("Expected 999 for new key, got %d, %v", val, ok)
	}
}

func TestLen(t *testing.T) {
	sm := New[string, int](16, StringHash)

	if sm.Len() != 0 {
		t.Error("Expected length 0 for empty map")
	}

	for i := 0; i < 100; i++ {
		sm.Set(strconv.Itoa(i), i)
	}

	if sm.Len() != 100 {
		t.Errorf("Expected length 100, got %d", sm.Len())
	}
}

func TestRange(t *testing.T) {
	sm := New[string, int](16, StringHash)

	for i := 0; i < 10; i++ {
		sm.Set(strconv.Itoa(i), i)
	}

	count := 0
	sum := 0
	sm.Range(func(k string, v int) bool {
		count++
		sum += v
		return true
	})

	if count != 10 {
		t.Errorf("Range iterated %d times, expected 10", count)
	}
	if sum != 45 { // 0+1+2+...+9 = 45
		t.Errorf("Sum was %d, expected 45", sum)
	}
}

func TestRange_EarlyExit(t *testing.T) {
	sm := New[string, int](16, StringHash)

	for i := 0; i < 100; i++ {
		sm.Set(strconv.Itoa(i), i)
	}

	count := 0
	sm.Range(func(k string, v int) bool {
		count++
		return count < 5 // Stop after 5
	})

	if count != 5 {
		t.Errorf("Range should have stopped after 5, got %d", count)
	}
}

func TestKeys(t *testing.T) {
	sm := New[string, int](16, StringHash)

	expected := map[string]bool{"a": true, "b": true, "c": true}
	for k := range expected {
		sm.Set(k, 1)
	}

	keys := sm.Keys()
	if len(keys) != 3 {
		t.Errorf("Expected 3 keys, got %d", len(keys))
	}

	for _, k := range keys {
		if !expected[k] {
			t.Errorf("Unexpected key: %s", k)
		}
	}
}

func TestValues(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("a", 1)
	sm.Set("b", 2)
	sm.Set("c", 3)

	values := sm.Values()
	if len(values) != 3 {
		t.Errorf("Expected 3 values, got %d", len(values))
	}

	sum := 0
	for _, v := range values {
		sum += v
	}
	if sum != 6 {
		t.Errorf("Sum of values should be 6, got %d", sum)
	}
}

func TestClear(t *testing.T) {
	sm := New[string, int](16, StringHash)

	for i := 0; i < 100; i++ {
		sm.Set(strconv.Itoa(i), i)
	}

	sm.Clear()

	if sm.Len() != 0 {
		t.Errorf("Expected length 0 after clear, got %d", sm.Len())
	}

	// Verify keys are gone
	for i := 0; i < 100; i++ {
		if _, ok := sm.Get(strconv.Itoa(i)); ok {
			t.Errorf("Key %d should be deleted after clear", i)
		}
	}
}

// =============================================================================
// HASH FUNCTION TESTS
// =============================================================================

func TestUInt64Hash(t *testing.T) {
	// Hash should be deterministic
	hash1 := UInt64Hash(12345)
	hash2 := UInt64Hash(12345)
	if hash1 != hash2 {
		t.Error("Hash should be deterministic")
	}

	// Different inputs should produce different outputs
	hash3 := UInt64Hash(12346)
	if hash1 == hash3 {
		t.Error("Different inputs should have different hashes")
	}
}

func TestStringHash(t *testing.T) {
	// Hash should be deterministic
	hash1 := StringHash("test")
	hash2 := StringHash("test")
	if hash1 != hash2 {
		t.Error("Hash should be deterministic")
	}

	// Different inputs should produce different outputs
	hash3 := StringHash("test2")
	if hash1 == hash3 {
		t.Error("Different inputs should have different hashes")
	}

	// Empty string should not panic
	_ = StringHash("")
}

func TestHashDistribution(t *testing.T) {
	sm := New[uint64, int](256, UInt64Hash)

	// Insert many keys
	for i := uint64(0); i < 10000; i++ {
		sm.Set(i, int(i))
	}

	// Check distribution across shards
	counts := make([]int, len(sm.shards))
	for i := range sm.shards {
		sm.shards[i].RLock()
		counts[i] = len(sm.shards[i].items)
		sm.shards[i].RUnlock()
	}

	// Verify reasonable distribution (no shard should have >5% of all items)
	maxExpected := 10000 / len(sm.shards) * 5 // 5x average
	for i, count := range counts {
		if count > maxExpected {
			t.Errorf("Shard %d has %d items, expected max %d", i, count, maxExpected)
		}
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestConcurrentSetGet(t *testing.T) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	var wg sync.WaitGroup
	const numGoroutines = 100
	const numOps = 1000

	// Writers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := id*numOps + j
				sm.Set(key, key*2)
			}
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := id*numOps + j
				sm.Get(key)
			}
		}(i)
	}

	wg.Wait()

	// Verify some writes succeeded
	if sm.Len() == 0 {
		t.Error("Expected some items to be written")
	}
}

func TestConcurrentGetOrSet(t *testing.T) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	var wg sync.WaitGroup
	var loaded atomic.Int64
	var set atomic.Int64

	const numGoroutines = 100

	// All goroutines try to GetOrSet the same key
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, wasLoaded := sm.GetOrSet(42, id)
			if wasLoaded {
				loaded.Add(1)
			} else {
				set.Add(1)
			}
		}(i)
	}

	wg.Wait()

	// Exactly one should have set, others should have loaded
	if set.Load() != 1 {
		t.Errorf("Expected exactly 1 set, got %d", set.Load())
	}
	if loaded.Load() != numGoroutines-1 {
		t.Errorf("Expected %d loads, got %d", numGoroutines-1, loaded.Load())
	}
}

func TestConcurrentUpdate(t *testing.T) {
	sm := New[string, int](256, StringHash)
	sm.Set("counter", 0)

	var wg sync.WaitGroup
	const numGoroutines = 100
	const numIncrements = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIncrements; j++ {
				sm.Update("counter", func(v int, ok bool) int {
					return v + 1
				})
			}
		}()
	}

	wg.Wait()

	val, _ := sm.Get("counter")
	expected := numGoroutines * numIncrements
	if val != expected {
		t.Errorf("Counter should be %d, got %d", expected, val)
	}
}

func TestConcurrentMixedOperations(t *testing.T) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	var wg sync.WaitGroup
	const numGoroutines = 50

	// Mixed operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := (id * 100 + j) % 1000

				switch j % 5 {
				case 0:
					sm.Set(key, j)
				case 1:
					sm.Get(key)
				case 2:
					sm.Delete(key)
				case 3:
					sm.GetOrSet(key, j)
				case 4:
					sm.Update(key, func(v int, ok bool) int { return v + 1 })
				}
			}
		}(i)
	}

	wg.Wait()

	// Should complete without deadlock or panic
	t.Logf("Final map size: %d", sm.Len())
}

func TestConcurrentRangeAndWrite(t *testing.T) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	// Pre-populate
	for i := 0; i < 1000; i++ {
		sm.Set(i, i)
	}

	var wg sync.WaitGroup

	// Readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sm.Range(func(k, v int) bool {
					return true
				})
			}
		}()
	}

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sm.Set(id*100+j, j)
			}
		}(i)
	}

	wg.Wait()
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestEmptyKey(t *testing.T) {
	sm := New[string, int](16, StringHash)

	sm.Set("", 100)
	val, ok := sm.Get("")
	if !ok || val != 100 {
		t.Error("Empty string key must be supported")
	}
}

func TestNilValue(t *testing.T) {
	sm := New[string, *int](16, StringHash)

	sm.Set("key", nil)
	val, ok := sm.Get("key")
	if !ok {
		t.Error("Nil value should be storable")
	}
	if val != nil {
		t.Error("Retrieved value should be nil")
	}
}

func TestZeroKey(t *testing.T) {
	sm := New[int, string](16, func(k int) uint64 { return uint64(k) })

	sm.Set(0, "zero")
	val, ok := sm.Get(0)
	if !ok || val != "zero" {
		t.Error("Zero key must be supported")
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkSet(b *testing.B) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Set(i, i)
	}
}

func BenchmarkGet(b *testing.B) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	for i := 0; i < 10000; i++ {
		sm.Set(i, i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Get(i % 10000)
	}
}

func BenchmarkConcurrentGet(b *testing.B) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	for i := 0; i < 10000; i++ {
		sm.Set(i, i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sm.Get(i % 10000)
			i++
		}
	})
}

func BenchmarkConcurrentMixed(b *testing.B) {
	sm := New[int, int](256, func(k int) uint64 { return uint64(k) })

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				sm.Set(i%10000, i)
			} else {
				sm.Get(i % 10000)
			}
			i++
		}
	})
}
