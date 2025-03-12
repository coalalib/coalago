package coalago

import (
	"time"

	"github.com/coalalib/coalago/session"
	"github.com/patrickmn/go-cache"
)

type sessionStorage interface {
	Set(sender string, receiver string, proxy string, sess session.SecuredSession)
	Get(sender string, receiver string, proxy string) (session.SecuredSession, bool)
	Delete(sender string, receiver string, proxy string)
	ItemCount() int
	LoadOrStore(sender string, receiver string, proxy string, sess session.SecuredSession) (session.SecuredSession, bool)
}

type sessionStorageImpl struct {
	storage *cache.Cache
}

func newSessionStorageImpl() *sessionStorageImpl {
	s := new(sessionStorageImpl)
	s.storage = cache.New(SESSIONS_POOL_EXPIRATION, time.Second*1)
	return s
}

func (s *sessionStorageImpl) Set(sender string, receiver string, proxy string, sess session.SecuredSession) {
	if len(proxy) != 0 {
		sender = ""
	}
	s.storage.SetDefault(sender+receiver+proxy, sess)
}

func (s *sessionStorageImpl) Get(sender string, receiver string, proxy string) (session.SecuredSession, bool) {
	if len(proxy) != 0 {
		sender = ""
	}
	v, ok := s.storage.Get(sender + receiver + proxy)
	if ok {
		return v.(session.SecuredSession), true
	}
	return session.SecuredSession{}, false
}

func (s *sessionStorageImpl) Delete(sender string, receiver string, proxy string) {
	if len(proxy) != 0 {
		sender = ""
	}
	s.storage.Delete(sender + receiver + proxy)
}

func (s *sessionStorageImpl) ItemCount() int {
	return s.storage.ItemCount()
}

func (s *sessionStorageImpl) LoadOrStore(sender string, receiver string, proxy string, sess session.SecuredSession) (session.SecuredSession, bool) {
	if len(proxy) != 0 {
		sender = ""
	}
	key := sender + receiver + proxy
	if v, ok := s.storage.Get(key); ok {
		return v.(session.SecuredSession), true
	}
	s.storage.SetDefault(key, sess)
	return sess, false
}

type proxySessionStorage struct {
	storage *cache.Cache
}

func newProxySessionStorage() *proxySessionStorage {
	s := new(proxySessionStorage)
	s.storage = cache.New(time.Minute, time.Second*1)
	return s
}

func (s *proxySessionStorage) Set(key string, value interface{}) {
	s.storage.SetDefault(key, value)
}

func (s *proxySessionStorage) Get(key string) (interface{}, bool) {
	v, ok := s.storage.Get(key)
	if ok {
		return v, true
	}
	return "", false
}

func (s *proxySessionStorage) Delete(key string) {
	s.storage.Delete(key)
}
