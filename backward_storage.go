package coalago

import (
	"errors"
	"sync"
	"time"
)

type backwardStorage struct {
	m  map[uint16]chan *CoAPMessage
	mx sync.RWMutex
}

func (b *backwardStorage) Has(id uint16) bool {
	b.mx.RLock()
	defer b.mx.RUnlock()
	_, ok := b.m[id]
	return ok
}

func (b *backwardStorage) Write(msg *CoAPMessage) {
	b.mx.Lock()
	defer b.mx.Unlock()
	ch, ok := b.m[msg.MessageID]
	if !ok {
		return
	}

	select {
	case ch <- msg:
	default:
	}
}

func (b *backwardStorage) Read(id uint16) (*CoAPMessage, error) {
	ch := make(chan *CoAPMessage)
	b.mx.Lock()
	b.m[id] = ch
	b.mx.Unlock()

	defer func() {
		b.mx.Lock()
		close(ch)
		delete(b.m, id)
		b.mx.Unlock()
	}()

	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(time.Second * 5):
		return nil, errors.New("timeout")
	}
}
