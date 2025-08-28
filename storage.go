package coalago

import (
	"net"
	"sync"
	"time"

	"github.com/coalalib/coalago/session"
)

const shardCount = 64

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
}

type shardedCache struct {
	shards [shardCount]*sync.Map
	ttl    time.Duration
}

func newShardedCache(ttl time.Duration) *shardedCache {
	c := &shardedCache{ttl: ttl}
	for i := 0; i < shardCount; i++ {
		c.shards[i] = &sync.Map{}
	}
	go c.cleanupLoop()
	return c
}

func (c *shardedCache) shard(key string) *sync.Map {
	h := fnv32(key)
	return c.shards[h%shardCount]
}

func (c *shardedCache) Set(key string, val interface{}) {
	item := cacheItem{value: val, expiresAt: time.Now().Add(c.ttl)}
	c.shard(key).Store(key, item)
}

func (c *shardedCache) Get(key string) (interface{}, bool) {
	sh := c.shard(key)
	v, ok := sh.Load(key)
	if !ok {
		return nil, false
	}
	item := v.(cacheItem)
	if time.Now().After(item.expiresAt) {
		sh.Delete(key)
		return nil, false
	}
	return item.value, true
}

func (c *shardedCache) Delete(key string) {
	c.shard(key).Delete(key)
}

func (c *shardedCache) ItemCount() int {
	total := 0
	for _, shard := range c.shards {
		shard.Range(func(_, v interface{}) bool {
			item := v.(cacheItem)
			if time.Now().Before(item.expiresAt) {
				total++
			}
			return true
		})
	}
	return total
}

func (c *shardedCache) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		for _, shard := range c.shards {
			shard.Range(func(k, v interface{}) bool {
				item := v.(cacheItem)
				if time.Now().After(item.expiresAt) {
					shard.Delete(k)
				}
				return true
			})
		}
	}
}

func fnv32(key string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		hash *= 16777619
		hash ^= uint32(key[i])
	}
	return hash
}

// sessionStorageImpl using shardedCache

type sessionStorageImpl struct {
	storage *shardedCache
}

func newSessionStorageImpl(ttl time.Duration) *sessionStorageImpl {
	return &sessionStorageImpl{
		storage: newShardedCache(ttl),
	}
}

func (s *sessionStorageImpl) Set(sender, receiver, proxy string, sess session.SecuredSession) {
	if proxy != "" {
		sender = ""
	}
	s.storage.Set(sender+receiver+proxy, sess)
}

func (s *sessionStorageImpl) Get(sender, receiver, proxy string) (session.SecuredSession, bool) {
	if proxy != "" {
		sender = ""
	}
	v, ok := s.storage.Get(sender + receiver + proxy)
	if !ok {
		return session.SecuredSession{}, false
	}
	return v.(session.SecuredSession), true
}

func (s *sessionStorageImpl) Delete(sender, receiver, proxy string) {
	if proxy != "" {
		sender = ""
	}
	s.storage.Delete(sender + receiver + proxy)
}

func (s *sessionStorageImpl) LoadOrStore(sender, receiver, proxy string, sess session.SecuredSession) (session.SecuredSession, bool) {
	if proxy != "" {
		sender = ""
	}
	key := sender + receiver + proxy
	if v, ok := s.storage.Get(key); ok {
		return v.(session.SecuredSession), true
	}
	s.storage.Set(key, sess)
	return sess, false
}

func (s *sessionStorageImpl) ItemCount() int {
	return s.storage.ItemCount()
}

// proxySessionStorage using shardedCache

type proxySessionStorage struct {
	storage *shardedCache
}

func newProxySessionStorage(ttl time.Duration) *proxySessionStorage {
	return &proxySessionStorage{
		storage: newShardedCache(ttl),
	}
}

func (s *proxySessionStorage) Set(key string, value interface{}) {
	s.storage.Set(key, value)
}

func (s *proxySessionStorage) Get(key string) (interface{}, bool) {
	return s.storage.Get(key)
}

func (s *proxySessionStorage) Delete(key string) {
	s.storage.Delete(key)
}

func (s *proxySessionStorage) ItemCount() int {
	return s.storage.ItemCount()
}

type connectionStorage struct {
	storage *shardedCache
}

func newConnectionStorage(ttl time.Duration) *connectionStorage {
	return &connectionStorage{
		storage: newShardedCache(ttl),
	}
}

func (c *connectionStorage) SetTCP(addr string, conn net.Conn) {
	c.storage.Set("tcp:"+addr, conn)
}

func (c *connectionStorage) GetTCP(addr string) (net.Conn, bool) {
	v, ok := c.storage.Get("tcp:" + addr)
	if !ok {
		return nil, false
	}
	return v.(net.Conn), true
}

func (c *connectionStorage) DeleteTCP(addr string) {
	c.storage.Delete("tcp:" + addr)
}
