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
		i := i
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
		i := i
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
		i := i
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
