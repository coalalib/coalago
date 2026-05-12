package coalago

import (
	"net"
	"testing"
	"time"
)

func TestShardedCacheSetGetDelete(t *testing.T) {
	cache := newShardedCache(time.Minute)

	cache.Set("key", "value")
	got, ok := cache.Get("key")
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got != "value" {
		t.Fatalf("Get() = %v, want value", got)
	}

	cache.Delete("key")
	if _, ok := cache.Get("key"); ok {
		t.Fatal("Get() ok = true after Delete, want false")
	}
}

func TestShardedCacheExpiration(t *testing.T) {
	cache := newShardedCache(10 * time.Millisecond)

	cache.Set("key", "value")
	time.Sleep(25 * time.Millisecond)

	if _, ok := cache.Get("key"); ok {
		t.Fatal("Get() ok = true after TTL, want false")
	}
}

func TestShardedCacheLoadOrStore(t *testing.T) {
	cache := newShardedCache(time.Minute)

	got, loaded := cache.LoadOrStore("key", "first")
	if loaded {
		t.Fatal("LoadOrStore() loaded = true for new value, want false")
	}
	if got != "first" {
		t.Fatalf("LoadOrStore() = %v, want first", got)
	}

	got, loaded = cache.LoadOrStore("key", "second")
	if !loaded {
		t.Fatal("LoadOrStore() loaded = false for existing value, want true")
	}
	if got != "first" {
		t.Fatalf("LoadOrStore() = %v, want first", got)
	}
}

func TestBackwardStorageWriteDeliversToPendingRead(t *testing.T) {
	storage := &backwardStorage{m: make(map[string]chan *CoAPMessage)}
	sender := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5683}
	message := NewCoAPMessage(ACK, CoapCodeContent)
	message.Token = []byte("tok")
	message.Sender = sender
	id := message.GetTokenString() + sender.String()

	result := make(chan *CoAPMessage, 1)
	errs := make(chan error, 1)

	go func() {
		msg, err := storage.Read(id)
		if err != nil {
			errs <- err
			return
		}
		result <- msg
	}()

	deadline := time.Now().Add(time.Second)
	for {
		storage.mx.RLock()
		_, ok := storage.m[id]
		storage.mx.RUnlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Read() did not register channel")
		}
		time.Sleep(time.Millisecond)
	}

	storage.Write(message)

	select {
	case err := <-errs:
		t.Fatalf("Read() returned error: %v", err)
	case got := <-result:
		if got != message {
			t.Fatalf("Read() returned %p, want %p", got, message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}

	storage.mx.RLock()
	_, ok := storage.m[id]
	storage.mx.RUnlock()
	if ok {
		t.Fatal("Read() left channel in storage after returning")
	}
}
