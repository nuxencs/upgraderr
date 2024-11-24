package ttlcache

import (
	"time"

	"github.com/titlerr/upgraderr/pkg/timecache"
)

func New[K comparable, V any](options Options[K, V]) *Cache[K, V] {
	c := Cache[K, V]{
		o:  options,
		ch: make(chan time.Time, 1000),
		m:  make(map[K]Item[V]),
	}

	if options.defaultTTL != NoTTL && options.defaultResolution == 0 {
		c.tc = *timecache.New(timecache.Options{}.Round(options.defaultTTL / 2))
	} else if options.defaultResolution != 0 {
		c.tc = *timecache.New(timecache.Options{}.Round(options.defaultResolution))
	}

	go c.startExpirations()
	return &c
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
	it, ok := c.get(key)
	if !ok {
		return *new(V), ok
	}

	if !c.o.noUpdateTime && !it.t.IsZero() && c.getDuration(it.d).After(it.t) {
		c.set(key, it)
	}

	return it.v, ok
}

func (c *Cache[K, V]) GetItem(key K) (Item[V], bool) {
	it, ok := c.get(key)
	if !ok {
		return it, ok
	}

	if !c.o.noUpdateTime && !it.t.IsZero() && c.getDuration(it.d).After(it.t) {
		c.set(key, it)
	}

	return it, ok
}

func (c *Cache[K, V]) Set(key K, value V, duration time.Duration) bool {
	if c.o.defaultTTL == NoTTL && duration == DefaultTTL {
		duration = NoTTL
	}

	c.set(key, Item[V]{v: value, d: duration})
	return true
}

func (c *Cache[K, V]) Delete(key K) {
	c.delete(key, ReasonDeleted)
}

func (c *Cache[K, V]) GetKeys() []K {
	return c.getkeys()
}

func (c *Cache[K, V]) Close() {
	c.close()
}

func (i *Item[V]) GetDuration() time.Duration {
	return i.getDuration()
}

func (i *Item[V]) GetTime() time.Time {
	return i.getTime()
}

func (i *Item[V]) GetValue() V {
	return i.getValue()
}

func (o Options[K, V]) SetTimerResolution(d time.Duration) Options[K, V] {
	o.defaultResolution = d
	return o
}

func (o Options[K, V]) SetDefaultTTL(d time.Duration) Options[K, V] {
	o.defaultTTL = d
	return o
}

func (o Options[K, V]) SetDeallocationFunc(f DeallocationFunc[K, V]) Options[K, V] {
	o.deallocationFunc = f
	return o
}

func (o Options[K, V]) DisableUpdateTime(val bool) Options[K, V] {
	o.noUpdateTime = val
	return o
}
