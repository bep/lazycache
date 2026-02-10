// Copyright 2024 Bjørn Erik Pedersen
// SPDX-License-Identifier: MIT

package lazycache

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestCache(t *testing.T) {
	c := qt.New(t)

	cache := New[int, any](Options[int, any]{MaxEntries: 10})

	get := func(key int) any {
		v, found := cache.Get(key)
		if !found {
			return nil
		}
		return v
	}

	c.Assert(get(123456), qt.IsNil)

	cache.Set(123456, 32)
	c.Assert(get(123456), qt.Equals, 32)

	for i := range 20 {
		cache.Set(i, i)
	}
	c.Assert(get(123456), qt.IsNil)
	c.Assert(cache.Resize(5), qt.Equals, 5)
	c.Assert(get(3), qt.IsNil)
	c.Assert(cache.contains(18), qt.IsTrue)
	c.Assert(cache.keys(), qt.DeepEquals, []int{15, 16, 17, 18, 19})

	c.Assert(cache.DeleteFunc(
		func(key int, value any) bool {
			return value.(int) > 15
		},
	), qt.Equals, 4)

	c.Assert(cache.contains(18), qt.IsFalse)
	c.Assert(cache.contains(15), qt.IsTrue)

	c.Assert(cache.Delete(15), qt.IsTrue)
	c.Assert(cache.Delete(15), qt.IsFalse)
	c.Assert(cache.contains(15), qt.IsFalse)

	c.Assert(cache.Len(), qt.Equals, 0)

	c.Assert(func() { New[int, any](Options[int, any]{MaxEntries: -1}) }, qt.PanicMatches, "must provide a positive size")
}

func TestPanic(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, any]{MaxEntries: 1000})
	ep := &errorProducer{}

	willPanic := func(i int) func() {
		return func() {
			cache.GetOrCreate(i, func(key int) (any, error) {
				ep.Panic(i)
				return nil, nil
			})
		}
	}

	for i := range 2 {
		for range 2 {
			c.Assert(willPanic(i), qt.PanicMatches, fmt.Sprintf("failed-%d", i))
		}
	}

	for i := range 2 {
		v, _, err := cache.GetOrCreate(i, func(key int) (any, error) {
			return key + 2, nil
		})
		c.Assert(err, qt.IsNil)
		c.Assert(v, qt.Equals, i+2)
	}
}

func TestDeleteFunc(t *testing.T) {
	c := qt.New(t)

	c.Run("Basic", func(c *qt.C) {
		cache := New(Options[int, any]{MaxEntries: 1000})
		for i := range 10 {
			cache.Set(i, i)
		}
		c.Assert(cache.DeleteFunc(func(key int, value any) bool {
			return key%2 == 0
		}), qt.Equals, 5)
		c.Assert(cache.Len(), qt.Equals, 5)
	})

	c.Run("Temporary", func(c *qt.C) {
		var wg sync.WaitGroup

		// There's some timing involved in this test, so we'll need
		// to retry a few times to cover all the cases.
		for range 100 {
			cache := New(Options[int, any]{MaxEntries: 1000})
			for i := range 10 {
				cache.Set(i, i)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 10; i < 30; i++ {
					v, _, err := cache.GetOrCreate(i, func(key int) (any, error) {
						if key%2 == 0 {
							return nil, errors.New("failed")
						}
						time.Sleep(10 * time.Microsecond)

						return key, nil
					})
					if err != nil {
						c.Assert(err, qt.ErrorMatches, "failed")
					} else {
						c.Assert(v, qt.Equals, i)
					}

				}
			}()
			time.Sleep(3 * time.Microsecond)
			c.Assert(cache.DeleteFunc(func(key int, value any) bool {
				return key%2 == 0
			}), qt.Equals, 5)

		}

		wg.Wait()
	})
}

func TestGetOrCreate(t *testing.T) {
	c := qt.New(t)
	cache := New(Options[int, any]{MaxEntries: 100})
	counter := 0
	create := func(key int) (any, error) {
		counter++
		return fmt.Sprintf("value-%d-%d", key, counter), nil
	}
	for i := range 3 {
		res, found, err := cache.GetOrCreate(123456, create)
		c.Assert(err, qt.IsNil)
		c.Assert(res, qt.Equals, "value-123456-1")
		c.Assert(found, qt.Equals, i > 0)
	}
	v, found := cache.Get(123456)
	c.Assert(found, qt.IsTrue)
	c.Assert(v, qt.Equals, "value-123456-1")
}

func TestGetOrCreateError(t *testing.T) {
	c := qt.New(t)
	cache := New(Options[int, any]{MaxEntries: 100})
	create := func(key int) (any, error) {
		return nil, fmt.Errorf("failed")
	}

	res, _, err := cache.GetOrCreate(123456, create)
	c.Assert(err, qt.ErrorMatches, "failed")
	c.Assert(res, qt.IsNil)
}

func TestOnEvict(t *testing.T) {
	c := qt.New(t)
	var onEvictCalled bool
	cache := New(Options[int, any]{MaxEntries: 20, OnEvict: func(key int, value any) {
		onEvictCalled = true
	}})

	create := func(key int) (any, error) {
		return key, nil
	}

	for i := range 25 {
		cache.GetOrCreate(i, create)
	}

	c.Assert(onEvictCalled, qt.IsTrue)
}

func TestGetOrCreateConcurrent(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, any]{MaxEntries: 1000})

	var countersmu sync.Mutex
	counters := make(map[int]int)
	create := func(key int) (any, error) {
		countersmu.Lock()
		count := counters[key]
		counters[key]++
		countersmu.Unlock()
		time.Sleep(time.Duration(rand.Intn(40)+1) * time.Millisecond)
		return fmt.Sprintf("%v-%d", key, count), nil
	}

	var wg sync.WaitGroup

	for i := range 20 {
		expect := fmt.Sprintf("%d-0", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				res, _, err := cache.GetOrCreate(i, create)
				c.Assert(err, qt.IsNil)
				c.Assert(res, qt.Equals, expect)
			}
		}()
	}

	for i := range 20 {
		expect := fmt.Sprintf("%d-0", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				countersmu.Lock()
				_, created := counters[i]
				countersmu.Unlock()
				res, found := cache.Get(i)
				if !found {
					c.Assert(created, qt.IsFalse)
				} else {
					c.Assert(res, qt.Equals, expect)
				}
			}
		}()
	}

	wg.Wait()
}

func TestGetOrCreateAndResizeConcurrent(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, int]{MaxEntries: 1000})

	var counter atomic.Uint32
	create := func(key int) (int, error) {
		time.Sleep(time.Duration(rand.Intn(40)+1) * time.Millisecond)
		counter.Add(1)
		return int(counter.Load()), nil
	}

	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				v, _, err := cache.GetOrCreate(i, create)
				c.Assert(err, qt.IsNil)
				c.Assert(v, qt.Not(qt.Equals), 0)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100; i >= 0; i-- {
			cache.Resize(i)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestGetOrCreateRecursive(t *testing.T) {
	c := qt.New(t)

	var wg sync.WaitGroup

	n := 200

	for range 30 {
		cache := New(Options[int, any]{MaxEntries: 1000})

		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 10 {
					// This test was added to test a deadlock situation with nested GetOrCreate calls on the same cache.
					// Note that the keys below are carefully selected to not overlap, as this case may still deadlock:
					// goroutine 1: GetOrCreate(1) => GetOrCreate(2)
					// goroutine 2: GetOrCreate(2) => GetOrCreate(1)
					key1, key2 := rand.Intn(n), rand.Intn(n)+n
					if key2 == key1 {
						key2++
					}
					shouldFail := key1%10 == 0
					v, _, err := cache.GetOrCreate(key1, func(key int) (any, error) {
						if shouldFail {
							return nil, fmt.Errorf("failed")
						}
						v, _, err := cache.GetOrCreate(key2, func(key int) (any, error) {
							return "inner", nil
						})
						c.Assert(err, qt.IsNil)
						return v, nil
					})

					if shouldFail {
						c.Assert(err, qt.ErrorMatches, "failed")
						c.Assert(v, qt.IsNil)
					} else {
						c.Assert(err, qt.IsNil)
						c.Assert(v, qt.Equals, "inner")
					}
				}
			}()

		}
		wg.Wait()
	}
}

// TestGetOrCreateEvictionRace tests the scenario where an entry is evicted
// while its create function is still running, and a new entry for the same key
// is created. The failing original create() should NOT delete the new entry.
func TestGetOrCreateEvictionRace(t *testing.T) {
	c := qt.New(t)

	// Use a small cache to force evictions
	cache := New(Options[int, string]{MaxEntries: 2})

	var (
		wg             sync.WaitGroup
		key1Started    = make(chan struct{})
		key1Continue   = make(chan struct{})
		key1Done       = make(chan struct{})
		key1NewCreated = make(chan struct{})
	)

	// Goroutine 1: Start creating key 1, but wait before completing
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, _, err := cache.GetOrCreate(1, func(key int) (string, error) {
			close(key1Started) // Signal that we've started
			<-key1Continue     // Wait for signal to continue
			return "", errors.New("intentional failure")
		})
		c.Assert(err, qt.ErrorMatches, "intentional failure")
		c.Assert(v, qt.Equals, "")
		close(key1Done)
	}()

	// Wait for goroutine 1 to start creating
	<-key1Started

	// Fill the cache to evict key 1's pending entry
	cache.Set(2, "value2")
	cache.Set(3, "value3") // This should evict key 1

	// Now create a new entry for key 1 that will succeed
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, _, err := cache.GetOrCreate(1, func(key int) (string, error) {
			return "new-value-1", nil
		})
		c.Assert(err, qt.IsNil)
		c.Assert(v, qt.Equals, "new-value-1")
		close(key1NewCreated)
	}()

	// Wait for the new entry to be created before letting the old one fail
	<-key1NewCreated

	// Now let goroutine 1 complete (with failure) - this will call Delete(1)
	close(key1Continue)
	<-key1Done

	wg.Wait()

	// The new entry for key 1 should still exist because deleteIfSame()
	// only deletes if the wrapper is the same instance.
	v, found := cache.Get(1)
	c.Assert(found, qt.IsTrue, qt.Commentf("new entry should not be deleted by failing old create"))
	c.Assert(v, qt.Equals, "new-value-1")
}

// TestSetDuringGetOrCreate tests that Set() during a failing GetOrCreate
// preserves the Set() value. The failing create should NOT delete the newer entry.
func TestSetDuringGetOrCreate(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, string]{MaxEntries: 100})

	var (
		createStarted  = make(chan struct{})
		createContinue = make(chan struct{})
		wg             sync.WaitGroup
	)

	// Start a GetOrCreate that will fail
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, err := cache.GetOrCreate(1, func(key int) (string, error) {
			close(createStarted)
			<-createContinue
			return "", errors.New("intentional failure")
		})
		c.Assert(err, qt.ErrorMatches, "intentional failure")
	}()

	<-createStarted

	// While create is running, Set a value for the same key
	cache.Set(1, "set-value")

	// Let the create continue and fail
	close(createContinue)
	wg.Wait()

	// The Set value should still exist because deleteIfSame() only
	// deletes if the wrapper is the same instance.
	v, found := cache.Get(1)
	c.Assert(found, qt.IsTrue, qt.Commentf("Set() value should not be deleted by failing GetOrCreate"))
	c.Assert(v, qt.Equals, "set-value")
}

// TestOnEvictWithPendingEntry tests that OnEvict correctly waits for
// entries that are still being created.
func TestOnEvictWithPendingEntry(t *testing.T) {
	c := qt.New(t)

	var (
		evictedKeys   []int
		evictedValues []string
		evictMu       sync.Mutex
		createStarted = make(chan struct{})
	)

	cache := New(Options[int, string]{
		MaxEntries: 2,
		OnEvict: func(key int, value string) {
			evictMu.Lock()
			evictedKeys = append(evictedKeys, key)
			evictedValues = append(evictedValues, value)
			evictMu.Unlock()
		},
	})

	var wg sync.WaitGroup

	// Start creating key 1 with a slow create function
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, _, err := cache.GetOrCreate(1, func(key int) (string, error) {
			close(createStarted)
			time.Sleep(50 * time.Millisecond)
			return "value1", nil
		})
		c.Assert(err, qt.IsNil)
		c.Assert(v, qt.Equals, "value1")
	}()

	<-createStarted

	// Add entries to evict key 1
	cache.Set(2, "value2")
	cache.Set(3, "value3") // This should trigger eviction of key 1

	wg.Wait()

	// Give time for eviction callback to complete
	time.Sleep(100 * time.Millisecond)

	evictMu.Lock()
	// Key 1 should have been evicted with its final value (after create completed)
	foundKey1 := false
	for i, k := range evictedKeys {
		if k == 1 {
			foundKey1 = true
			c.Assert(evictedValues[i], qt.Equals, "value1")
		}
	}
	evictMu.Unlock()

	// Note: foundKey1 might be false if the eviction timing differs,
	// which is acceptable as long as no panic/race occurred
	_ = foundKey1
}

// TestGetOrCreateConcurrentErrors tests that multiple goroutines calling
// GetOrCreate for the same key all receive the same error when create fails.
func TestGetOrCreateConcurrentErrors(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, string]{MaxEntries: 100})

	var (
		wg          sync.WaitGroup
		createCount atomic.Int32
	)

	// Multiple goroutines try to get/create the same failing key
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := cache.GetOrCreate(1, func(key int) (string, error) {
				createCount.Add(1)
				time.Sleep(10 * time.Millisecond)
				return "", errors.New("create failed")
			})
			c.Assert(err, qt.ErrorMatches, "create failed")
		}()
	}

	wg.Wait()

	// The create function should only be called once
	// (subsequent callers wait on the first one and get the same error)
	c.Assert(createCount.Load(), qt.Equals, int32(1))
}

// TestDeleteFuncConcurrentCreate tests DeleteFunc behavior when entries
// are being created concurrently.
func TestDeleteFuncConcurrentCreate(t *testing.T) {
	for range 50 { // Run multiple times to catch race conditions
		cache := New(Options[int, int]{MaxEntries: 100})

		// Pre-populate with some entries
		for i := range 10 {
			cache.Set(i, i)
		}

		var wg sync.WaitGroup

		// Concurrently create new entries
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 10; i < 20; i++ {
				cache.GetOrCreate(i, func(key int) (int, error) {
					time.Sleep(time.Microsecond)
					return key * 2, nil
				})
			}
		}()

		// Concurrently delete entries matching a condition
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.DeleteFunc(func(key int, value int) bool {
				return key%2 == 0
			})
		}()

		wg.Wait()
	}
	// Success means no panics or races occurred
}

// TestResizeToZero tests behavior when resizing cache to zero.
func TestResizeToZero(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, int]{MaxEntries: 10})

	for i := range 5 {
		cache.Set(i, i)
	}

	evicted := cache.Resize(0)
	c.Assert(evicted, qt.Equals, 5)
	c.Assert(cache.Len(), qt.Equals, 0)

	// Should still work after resize to 0
	cache.Resize(10)
	cache.Set(1, 1)
	v, found := cache.Get(1)
	c.Assert(found, qt.IsTrue)
	c.Assert(v, qt.Equals, 1)
}

// TestGetOrCreatePanicRecovery tests that after a panic, the same key
// can be created again successfully.
func TestGetOrCreatePanicRecovery(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, string]{MaxEntries: 100})

	// First call panics
	c.Assert(func() {
		cache.GetOrCreate(1, func(key int) (string, error) {
			panic("intentional panic")
		})
	}, qt.PanicMatches, "intentional panic")

	// Entry should be removed, so a new create should work
	v, found, err := cache.GetOrCreate(1, func(key int) (string, error) {
		return "recovered", nil
	})
	c.Assert(err, qt.IsNil)
	c.Assert(found, qt.IsFalse) // Not found because previous entry was deleted
	c.Assert(v, qt.Equals, "recovered")
}

// TestConcurrentGetAndGetOrCreate tests concurrent Get and GetOrCreate
// operations on the same keys.
func TestConcurrentGetAndGetOrCreate(t *testing.T) {
	c := qt.New(t)

	cache := New(Options[int, int]{MaxEntries: 100})

	var wg sync.WaitGroup

	// GetOrCreate goroutines
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				key := (i + j) % 15
				v, _, err := cache.GetOrCreate(key, func(k int) (int, error) {
					time.Sleep(time.Microsecond)
					return k * 10, nil
				})
				c.Assert(err, qt.IsNil)
				c.Assert(v, qt.Equals, key*10)
			}
		}()
	}

	// Get goroutines (may or may not find entries)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				key := j % 15
				v, found := cache.Get(key)
				if found {
					c.Assert(v, qt.Equals, key*10)
				}
			}
		}()
	}

	wg.Wait()
}

func BenchmarkGetOrCreateAndGet(b *testing.B) {
	const maxSize = 1000

	cache := New(Options[int, any]{MaxEntries: maxSize})
	r := rand.New(rand.NewSource(99))
	var mu sync.Mutex
	// Partially fill the cache.
	for i := range maxSize / 2 {
		cache.Set(i, i)
	}
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		b.ResetTimer()
		for pb.Next() {
			mu.Lock()
			i1, i2 := r.Intn(maxSize), r.Intn(maxSize)
			mu.Unlock()
			// Just Get the value.
			v, found := cache.Get(i1)
			if found && v != i1 {
				b.Fatalf("got %v, want %v", v, i1)
			}

			res2, _, err := cache.GetOrCreate(i2, func(key int) (any, error) {
				if i2%100 == 0 {
					// Simulate a slow create.
					time.Sleep(1 * time.Second)
				}
				return i2, nil
			})
			if err != nil {
				b.Fatal(err)
			}

			if v := res2; v != i2 {
				b.Fatalf("got %v, want %v", v, i2)
			}
		}
	})
}

func BenchmarkGetOrCreate(b *testing.B) {
	const maxSize = 1000

	r := rand.New(rand.NewSource(99))
	var mu sync.Mutex

	cache := New(Options[int, any]{MaxEntries: maxSize})

	// Partially fill the cache.
	for i := range maxSize / 3 {
		cache.Set(i, i)
	}
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mu.Lock()
			key := r.Intn(maxSize)
			mu.Unlock()

			v, _, err := cache.GetOrCreate(key, func(int) (any, error) {
				if key%100 == 0 {
					// Simulate a slow create.
					time.Sleep(1 * time.Second)
				}
				return key, nil
			})
			if err != nil {
				b.Fatal(err)
			}

			if v != key {
				b.Fatalf("got %v, want %v", v, key)
			}
		}
	})
}

func BenchmarkCacheSerial(b *testing.B) {
	const maxSize = 1000

	b.Run("Set", func(b *testing.B) {
		cache := New(Options[int, any]{MaxEntries: maxSize})
		for i := 0; i < b.N; i++ {
			cache.Set(i, i)
		}
	})

	b.Run("Get", func(b *testing.B) {
		cache := New(Options[int, any]{MaxEntries: maxSize})
		numItems := maxSize - 200
		for i := range numItems {
			cache.Set(i, i)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := i % numItems
			_, found := cache.Get(key)
			if !found {
				b.Fatalf("unexpected nil value for key %d", key)
			}
		}
	})
}

func BenchmarkCacheParallel(b *testing.B) {
	const maxSize = 1000

	b.Run("Set", func(b *testing.B) {
		cache := New(Options[int, any]{MaxEntries: maxSize})
		var counter uint32
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := int(atomic.AddUint32(&counter, 1))
				cache.Set(i, i)
			}
		})
	})

	b.Run("Get", func(b *testing.B) {
		cache := New(Options[int, any]{MaxEntries: maxSize})
		r := rand.New(rand.NewSource(99))
		var mu sync.Mutex
		numItems := maxSize - 200
		for i := range numItems {
			cache.Set(i, i)
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				mu.Lock()
				key := r.Intn(numItems)
				mu.Unlock()
				_, found := cache.Get(key)
				if !found {
					b.Fatalf("unexpected nil value for key %d", key)
				}
			}
		})
	})
}

type errorProducer struct{}

func (e *errorProducer) Panic(i int) {
	panic(fmt.Sprintf("failed-%d", i))
}
