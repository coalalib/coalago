package coalago

import (
	"errors"
	"sync"
	"time"
)

var bq = &backwardStorage{
	m: make(map[string]chan *CoAPMessage),
}

type backwardStorage struct {
	m  map[string]chan *CoAPMessage
	mx sync.RWMutex
}

func (b *backwardStorage) Has(msg *CoAPMessage) bool {
	b.mx.RLock()
	defer b.mx.RUnlock()
	_, ok := b.m[msg.GetTokenString()+msg.Sender.String()]
	return ok
}

func (b *backwardStorage) Write(msg *CoAPMessage) {
	b.mx.RLock()
	ch, ok := b.m[msg.GetTokenString()+msg.Sender.String()]
	b.mx.RUnlock()

	if !ok {
		return
	}

	// Проверяем, что канал не закрыт
	select {
	case ch <- msg:
	default:
		// Канал может быть закрыт или полон
	}
}

func (b *backwardStorage) Read(id string) (*CoAPMessage, error) {
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

func (b *backwardStorage) Get(id string) chan *CoAPMessage {
	b.mx.Lock()
	defer b.mx.Unlock()

	ch, ok := b.m[id]
	if !ok {
		ch = make(chan *CoAPMessage)
		b.m[id] = ch
	}
	return ch
}

func (b *backwardStorage) Delete(id string) {
	b.mx.Lock()
	defer b.mx.Unlock()
	if ch, ok := b.m[id]; ok {
		select {
		case <-ch:
			// Канал уже закрыт
		default:
			close(ch)
		}
		delete(b.m, id)
	}
}
