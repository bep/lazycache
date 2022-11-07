package lazycache

import (
	"sync"

	"github.com/hashicorp/golang-lru/simplelru"
)

var _ = Entry(&delayedEntry{})

// New creates a new LazyCache.
func New(options CacheOptions) *LazyCache {
	lru, err := simplelru.NewLRU(int(options.MaxEntries), nil)
	if err != nil {
		panic(err)
	}
	c := &LazyCache{
		lru: lru,
	}
	return c
}

type CacheOptions struct {
	// MaxEntries is the maximum number of entries that the cache should hold.
	// Note that this can also be adjusted after the cache is created with Resize.
	MaxEntries int
}

// Entry is the result of a cache lookup.
// Any Err value is the error that was returned by the cache prime function. This error value is cached. TODO(bep) consider this.
type Entry interface {
	Value() any
	Err() error
}

type LazyCache struct {
	lru *simplelru.LRU
	mu  sync.RWMutex
}

// Contains returns true if the given key is in the cache.
func (c *LazyCache) Contains(key any) bool {
	c.mu.RLock()
	b := c.lru.Contains(key)
	c.mu.RUnlock()
	return b
}

// Delete deletes the item with given key from the cache, returning if the
// key was contained.
func (c *LazyCache) Delete(key any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Remove(key)
}

// DeleteFunc deletes all entries for which the given function returns true.
func (c *LazyCache) DeleteFunc(matches func(key any, item Entry) bool) int {
	c.mu.RLock()
	keys := c.lru.Keys()

	var keysToDelete []any
	for _, key := range keys {
		v, _ := c.lru.Peek(key)
		if matches(key, v.(Entry)) {
			keysToDelete = append(keysToDelete, key)
		}
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	var deleteCount int
	for _, key := range keysToDelete {
		if c.lru.Remove(key) {
			deleteCount++
		}
	}

	return deleteCount
}

// Keys returns a slice of the keys in the cache, oldest first.
func (c *LazyCache) Keys() []any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Keys()
}

// Len returns the number of items in the cache.
func (c *LazyCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}

// Get returns the value associated with key.
func (c *LazyCache) Get(key any) Entry {
	c.mu.Lock()
	v, ok := c.lru.Get(key)
	c.mu.Unlock()
	if !ok {
		return entry{}
	}
	return v.(Entry)
}

// GetOrCreate returns the value associated with key, or creates it if it doesn't.
// Note that create, the cache prime function, is called once and then not called again for a given key
// unless the cache entry is evicted; it does not block other goroutines from calling GetOrCreate,
// it is not called with the cache lock held.
func (c *LazyCache) GetOrCreate(key any, create func(key any) (any, error)) Entry {
	c.mu.Lock()
	v, ok := c.lru.Get(key)
	if ok {
		c.mu.Unlock()
		return v.(Entry)
	}

	var e = &delayedEntry{
		done: make(chan struct{}),
	}
	// Add the *delayedEntry early and release the lock.
	// Calllers coming in getting the same cache entry will block on the done channel.
	c.lru.Add(key, e)
	c.mu.Unlock()

	// Create the  value with the lock released.
	v, err := create(key)

	// e is a pointer, and these values will be available to other callers getting this cache entry,
	// once the done channel is closed.
	e.err = err
	e.value = v
	close(e.done)

	return e
}

// Resize changes the cache size and returns the number of entries evicted.
func (c *LazyCache) Resize(size int) (evicted int) {
	c.mu.Lock()
	evicted = c.lru.Resize(size)
	c.mu.Unlock()
	return evicted
}

// Set associates value with key.
func (c *LazyCache) Set(key, value any) {
	c.mu.Lock()
	if _, ok := value.(Entry); !ok {
		value = entry{
			value: value,
		}
	}
	c.lru.Add(key, value)
	c.mu.Unlock()
}

// delayedEntry holds a cache value or error that is not available until the done channel is closed.
type delayedEntry struct {
	done  chan struct{}
	value any
	err   error
}

func (r *delayedEntry) Value() any {
	<-r.done
	return r.value
}

func (r *delayedEntry) Err() error {
	<-r.done
	return r.err
}

type entry struct {
	value any
	err   error
}

func (r entry) Value() any {
	return r.value
}

func (r entry) Err() error {
	return r.err
}
