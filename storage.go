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

func (c *shardedCache) LoadOrStore(key string, value interface{}) (interface{}, bool) {
	sh := c.shard(key)
	item := cacheItem{value: value, expiresAt: time.Now().Add(c.ttl)}

	// Пытаемся загрузить существующий элемент
	if v, ok := sh.Load(key); ok {
		existingItem := v.(cacheItem)
		if time.Now().Before(existingItem.expiresAt) {
			return existingItem.value, true
		}
		// Элемент истек, удаляем его
		sh.Delete(key)
	}

	// Сохраняем новый элемент
	sh.Store(key, item)
	return value, false
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

// assembleBlocks склеивает принятые ARQ-блоки в один буфер. Емкость считается заранее:
// последовательный append в цикле на больших телах (selftest ~2 МБ это 1500+ блоков)
// многократно реаллоцирует и копирует буфер.
func assembleBlocks(buf map[int][]byte, totalBlocks int) []byte {
	size := 0
	for i := 0; i < totalBlocks; i++ {
		size += len(buf[i])
	}
	b := make([]byte, 0, size)
	for i := 0; i < totalBlocks; i++ {
		b = append(b, buf[i]...)
	}
	return b
}

func fnv32(key string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		hash *= 16777619
		hash ^= uint32(key[i])
	}
	return hash
}

// sessionStorageImpl — шифрованные сессии по (локальный адрес, адрес пира, прокси).
// Обращение к сессии идёт на каждое coaps-сообщение (расшифровка входящего и
// шифрование ответа), поэтому хранилище типизированное: сессия лежит в карте как есть,
// без упаковки в interface{} на каждую запись, срок продлевается на месте, без
// повторной записи, а поиск собирает ключ в буфере на стеке, без аллокации.
type sessionStorageImpl struct {
	ttl    int64
	shards [shardCount]sessionShard
}

type sessionShard struct {
	mu sync.Mutex
	m  map[string]*sessionEntry
}

type sessionEntry struct {
	sess    session.SecuredSession
	expires int64 // под mu шарда
}

// sessionStorages tracks every session pool in the process (the global client pool and
// one per Server) so the sessions metric covers all of them.
var (
	sessionStoragesMu sync.Mutex
	sessionStorages   []*sessionStorageImpl
)

func newSessionStorageImpl(ttl time.Duration) *sessionStorageImpl {
	s := &sessionStorageImpl{ttl: int64(ttl)}
	for i := range s.shards {
		s.shards[i].m = make(map[string]*sessionEntry)
	}
	go s.cleanupLoop()
	sessionStoragesMu.Lock()
	sessionStorages = append(sessionStorages, s)
	sessionStoragesMu.Unlock()
	return s
}

// sessionsTotalCount sums the item counts of every session pool in the process.
func sessionsTotalCount() int {
	sessionStoragesMu.Lock()
	defer sessionStoragesMu.Unlock()
	total := 0
	for _, s := range sessionStorages {
		total += s.ItemCount()
	}
	return total
}

// sessionKey собирает ключ сессии в buf. Для проксированных пиров локальный адрес в
// ключ не входит.
func sessionKey(buf []byte, sender, receiver, proxy string) []byte {
	if proxy != "" {
		sender = ""
	}
	buf = append(buf, sender...)
	buf = append(buf, receiver...)
	return append(buf, proxy...)
}

func (s *sessionStorageImpl) shard(key []byte) *sessionShard {
	h := uint32(2166136261)
	for _, c := range key {
		h *= 16777619
		h ^= uint32(c)
	}
	return &s.shards[h%shardCount]
}

func (s *sessionStorageImpl) Set(sender, receiver, proxy string, sess session.SecuredSession) {
	var buf [128]byte
	key := sessionKey(buf[:0], sender, receiver, proxy)
	sh := s.shard(key)
	e := &sessionEntry{sess: sess, expires: monoNow() + s.ttl}
	sh.mu.Lock()
	sh.m[string(key)] = e
	sh.mu.Unlock()
}

func (s *sessionStorageImpl) Get(sender, receiver, proxy string) (session.SecuredSession, bool) {
	return s.get(sender, receiver, proxy, false)
}

// getRefresh — Get с продлением срока сессии, как у записи заново.
func (s *sessionStorageImpl) getRefresh(sender, receiver, proxy string) (session.SecuredSession, bool) {
	return s.get(sender, receiver, proxy, true)
}

func (s *sessionStorageImpl) get(sender, receiver, proxy string, refresh bool) (session.SecuredSession, bool) {
	var buf [128]byte
	key := sessionKey(buf[:0], sender, receiver, proxy)
	sh := s.shard(key)
	now := monoNow()

	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.m[string(key)]
	if !ok {
		return session.SecuredSession{}, false
	}
	if e.expires <= now {
		delete(sh.m, string(key))
		return session.SecuredSession{}, false
	}
	if refresh {
		e.expires = now + s.ttl
	}
	return e.sess, true
}

func (s *sessionStorageImpl) Delete(sender, receiver, proxy string) {
	var buf [128]byte
	key := sessionKey(buf[:0], sender, receiver, proxy)
	sh := s.shard(key)
	sh.mu.Lock()
	delete(sh.m, string(key))
	sh.mu.Unlock()
}

func (s *sessionStorageImpl) LoadOrStore(sender, receiver, proxy string, sess session.SecuredSession) (session.SecuredSession, bool) {
	if v, ok := s.Get(sender, receiver, proxy); ok {
		return v, true
	}
	s.Set(sender, receiver, proxy, sess)
	return sess, false
}

func (s *sessionStorageImpl) ItemCount() int {
	now := monoNow()
	total := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for _, e := range sh.m {
			if e.expires > now {
				total++
			}
		}
		sh.mu.Unlock()
	}
	return total
}

func (s *sessionStorageImpl) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := monoNow()
		for i := range s.shards {
			sh := &s.shards[i]
			sh.mu.Lock()
			for k, e := range sh.m {
				if e.expires <= now {
					delete(sh.m, k)
				}
			}
			sh.mu.Unlock()
		}
	}
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
