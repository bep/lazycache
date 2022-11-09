package lazycache

import (
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

	cache := New(CacheOptions{MaxEntries: 10})

	c.Assert(cache.Get("foo").Value(), qt.IsNil)

	cache.Set("foo", 32)
	c.Assert(cache.Get("foo").Value(), qt.Equals, 32)

	for i := 0; i < 20; i++ {
		cache.Set(i, i)
	}
	c.Assert(cache.Get("foo").Value(), qt.IsNil)
	c.Assert(cache.Resize(5), qt.Equals, 5)
	c.Assert(cache.Get(3).Value(), qt.IsNil)
	c.Assert(cache.Contains(18), qt.IsTrue)
	c.Assert(cache.Keys(), qt.DeepEquals, []any{15, 16, 17, 18, 19})

	c.Assert(cache.DeleteFunc(
		func(key any, value Entry) bool {
			return value.Value().(int) > 15
		},
	), qt.Equals, 4)

	c.Assert(cache.Contains(18), qt.IsFalse)
	c.Assert(cache.Contains(15), qt.IsTrue)

	c.Assert(cache.Delete(15), qt.IsTrue)
	c.Assert(cache.Delete(15), qt.IsFalse)
	c.Assert(cache.Contains(15), qt.IsFalse)

	c.Assert(cache.Len(), qt.Equals, 0)

	c.Assert(func() { New(CacheOptions{MaxEntries: -1}) }, qt.PanicMatches, "Must provide a positive size")

}

func TestGetOrCreate(t *testing.T) {
	c := qt.New(t)
	cache := New(CacheOptions{MaxEntries: 100})
	counter := 0
	create := func(key any) (any, error) {
		counter++
		return fmt.Sprintf("value-%s-%d", key, counter), nil
	}
	for i := 0; i < 3; i++ {
		res := cache.GetOrCreate("foo", create)
		c.Assert(res.Err(), qt.IsNil)
		c.Assert(res.Value(), qt.Equals, "value-foo-1")
	}
	c.Assert(cache.Get("foo").Value(), qt.Equals, "value-foo-1")
}

func TestGetOrCreateConcurrent(t *testing.T) {
	c := qt.New(t)

	cache := New(CacheOptions{MaxEntries: 1000})

	var countersmu sync.Mutex
	counters := make(map[any]int)
	create := func(key any) (any, error) {
		countersmu.Lock()
		count := counters[key]
		counters[key]++
		countersmu.Unlock()
		time.Sleep(time.Duration(rand.Intn(40)+1) * time.Millisecond)
		return fmt.Sprintf("%v-%d", key, count), nil
	}

	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		i := i
		expect := fmt.Sprintf("%d-0", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				res := cache.GetOrCreate(i, create)
				c.Assert(res.Err(), qt.IsNil)
				c.Assert(res.Value(), qt.Equals, expect)
			}
		}()
	}

	for i := 0; i < 20; i++ {
		i := i
		expect := fmt.Sprintf("%d-0", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				res := cache.Get(i)
				c.Assert(res.Err(), qt.IsNil)
				// The value may be nil if if GetOrCreate has not been called for i yet.
				if v := res.Value(); v != nil {
					c.Assert(v, qt.Equals, expect)
				}
			}
		}()
	}

	wg.Wait()
}

func BenchmarkGetOrCreate(b *testing.B) {
	const maxSize = 1000

	runBenchmark := func(b *testing.B, cache *Cache, getOrCreate func(key any, create func(key any) (any, error)) Entry) {
		r := rand.New(rand.NewSource(99))
		var mu sync.Mutex

		b.RunParallel(func(pb *testing.PB) {
			// Partially fill the cache.
			for i := 0; i < maxSize/2; i++ {
				cache.Set(i, i)
			}
			b.ResetTimer()
			for pb.Next() {
				mu.Lock()
				i1, i2 := r.Intn(maxSize), r.Intn(maxSize)
				mu.Unlock()
				// Just Get the value.
				res1 := cache.Get(i1)
				if err := res1.Err(); err != nil {
					b.Fatal(err)
				}
				if v := res1.Value(); v != nil && v != i1 {
					b.Fatalf("got %v, want %v", v, i1)
				}

				res2 := getOrCreate(i2, func(key any) (any, error) {
					if i2%100 == 0 {
						// Simulate a slow create.
						time.Sleep(1 * time.Second)
					}
					return i2, nil
				})

				if err := res2.Err(); err != nil {
					b.Fatal(err)
				}

				if v := res2.Value(); v != i2 {
					b.Fatalf("got %v, want %v", v, i2)
				}
			}
		})
	}

	/*b.Run("BaselineLock", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		runBenchmark(b, cache, cache.getOrCreateBaselineLock)
	})

	b.Run("BaselineDoubleCheckedLocking", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		runBenchmark(b, cache, cache.getOrCreateBaselDoubleCheckedLock)
	})*/

	b.Run("Real", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		runBenchmark(b, cache, cache.GetOrCreate)
	})

}

func BenchmarkCacheSerial(b *testing.B) {
	const maxSize = 1000

	b.Run("Set", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		for i := 0; i < b.N; i++ {
			cache.Set(i, i)
		}
	})

	b.Run("Get", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		numItems := maxSize - 200
		for i := 0; i < numItems; i++ {
			cache.Set(i, i)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := i % numItems
			v := cache.Get(key)
			if v == nil {
				b.Fatalf("unexpected nil value for key %d", key)
			}
		}
	})
}

func BenchmarkCacheParallel(b *testing.B) {
	const maxSize = 1000

	b.Run("Set", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		var counter uint32
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := int(atomic.AddUint32(&counter, 1))
				cache.Set(i, i)
			}
		})
	})

	b.Run("Get", func(b *testing.B) {
		cache := New(CacheOptions{MaxEntries: maxSize})
		r := rand.New(rand.NewSource(99))
		numItems := maxSize - 200
		for i := 0; i < numItems; i++ {
			cache.Set(i, i)
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				key := r.Intn(numItems)
				v := cache.Get(key)
				if v == nil {
					b.Fatalf("unexpected nil value for key %d", key)
				}
			}
		})
	})
}

// These are only used in benchmarks.
// This should be functionally equivalent to GetOrCreate.
func (c *Cache) getOrCreateBaselineLock(key any, create func(key any) (any, error)) Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.lru.Get(key)
	if ok {
		return v.(Entry)
	}

	v, err := create(key)

	e := entry{
		value: v,
		err:   err,
	}

	c.lru.Add(key, e)

	return e

}

// This variant does not hold any lock while calling create, which means it may be called multiple times for the same key.
func (c *Cache) getOrCreateBaselDoubleCheckedLock(key any, create func(key any) (any, error)) Entry {
	c.mu.Lock()
	v, ok := c.lru.Get(key)
	if ok {
		c.mu.Unlock()
		return v.(Entry)
	}
	c.mu.Unlock()

	v, err := create(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double check.
	v2, ok := c.lru.Get(key)
	if ok {
		return v2.(Entry)
	}

	e := entry{
		value: v,
		err:   err,
	}

	c.lru.Add(key, e)

	return e

}
