package ttlcache

import "time"

func (c *Cache[K, V]) get(key K) (Item[V], bool) {
	c.l.RLock()
	defer c.l.RUnlock()
	v, ok := c.m[key]

	if !ok {
		return v, ok
	}

	return v, ok
}

func (c *Cache[K, V]) set(key K, it Item[V]) Item[V] {
	it.d, it.t = c.getDuration(it.d)

	c.l.Lock()
	defer c.l.Unlock()
	c.m[key] = it
	c.ch <- it.t
	return it
}

func (c *Cache[K, V]) getOrSet(key K, it Item[V]) (Item[V], bool) {
	if v, ok := c.get(key); ok {
		return v, ok
	}

	return c.set(key, it), true
}

func (c *Cache[K, V]) delete(key K, reason DeallocationReason) {
	var v Item[V]
	c.l.Lock()
	defer c.l.Unlock()

	if c.o.deallocationFunc != nil {
		var ok bool
		v, ok = c.m[key]
		if !ok {
			return
		}
	}

	c.deleteUnsafe(key, v, reason)
}

func (c *Cache[K, V]) deleteUnsafe(key K, v Item[V], reason DeallocationReason) {
	delete(c.m, key)

	if c.o.deallocationFunc != nil {
		c.o.deallocationFunc(key, v.v, reason)
	}
}

func (c *Cache[K, V]) getkeys() []K {
	c.l.RLock()
	defer c.l.RUnlock()

	keys := make([]K, len(c.m))
	for k := range c.m {
		keys = append(keys, k)
	}

	return keys
}

func (c *Cache[K, V]) close() {
	c.l.Lock()
	defer c.l.Unlock()
	close(c.ch)
}

func (c *Cache[K, V]) getDuration(d time.Duration) (time.Duration, time.Time) {
	switch d {
	case NoTTL:
	case DefaultTTL:
		return c.o.defaultTTL, c.tc.Now().Add(c.o.defaultTTL)
	default:
		return d, c.tc.Now().Add(d)
	}

	return NoTTL, time.Time{}
}

func (i *Item[V]) getDuration() time.Duration {
	return i.d
}

func (i *Item[V]) getTime() time.Time {
	return i.t
}

func (i *Item[V]) getValue() V {
	return i.v
}
