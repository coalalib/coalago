package coalago

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// bq — ожидающие ответа запросы Server.Send: ответ (или PeerHello) с тем же адресом и
// токеном уходит в канал ожидающего вместо обработчика ресурса.
var bq = &backwardStorage{
	m: make(map[exKey]chan *CoAPMessage),
}

type backwardStorage struct {
	m  map[exKey]chan *CoAPMessage
	mx sync.RWMutex
	// n — число ожидающих: Has спрашивают на каждое входящее сообщение, а ожидающих
	// почти всегда нет.
	n atomic.Int64
}

func (b *backwardStorage) Has(msg *CoAPMessage) bool {
	if b.n.Load() == 0 {
		return false
	}
	k := messageKey(msg)
	b.mx.RLock()
	defer b.mx.RUnlock()
	_, ok := b.m[k]
	return ok
}

func (b *backwardStorage) Write(msg *CoAPMessage) {
	if b.n.Load() == 0 {
		return
	}
	k := messageKey(msg)
	b.mx.RLock()
	defer b.mx.RUnlock()

	ch, ok := b.m[k]

	if !ok {
		return
	}

	select {
	case ch <- msg:
	default:
		// Receiver is gone or not ready; this is a best-effort handoff.
	}
}

func (b *backwardStorage) Read(id exKey) (*CoAPMessage, error) {
	ch := make(chan *CoAPMessage)
	b.mx.Lock()
	if _, ok := b.m[id]; !ok {
		b.n.Add(1)
	}
	b.m[id] = ch
	b.mx.Unlock()

	// Remove from map before closing to prevent Write after close
	defer func() {
		b.mx.Lock()
		if b.m[id] == ch {
			delete(b.m, id)
			b.n.Add(-1)
			close(ch)
		}
		b.mx.Unlock()
	}()

	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(time.Second * 5):
		return nil, errors.New("timeout")
	}
}

func (b *backwardStorage) Get(id exKey) chan *CoAPMessage {
	b.mx.Lock()
	defer b.mx.Unlock()

	ch, ok := b.m[id]
	if !ok {
		ch = make(chan *CoAPMessage)
		b.m[id] = ch
		b.n.Add(1)
	}
	return ch
}

func (b *backwardStorage) Delete(id exKey) {
	b.mx.Lock()
	ch, ok := b.m[id]
	if ok {
		delete(b.m, id)
		b.n.Add(-1)
		close(ch)
	}
	b.mx.Unlock()
}
