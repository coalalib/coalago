package coalago

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Переиспользование сокетов клиентом (WithIdleConns): без него каждый запрос уходит с
// нового эфемерного порта, кэш сессий ключуется по (localAddr, remoteAddr) и не попадает
// ни разу - handshake на каждый запрос и по сессии на каждый порт у пира.

type recordingPeer struct {
	mx      sync.Mutex
	senders []string
}

func (p *recordingPeer) record(addr string) {
	p.mx.Lock()
	p.senders = append(p.senders, addr)
	p.mx.Unlock()
}

func (p *recordingPeer) distinctSenders() int {
	p.mx.Lock()
	defer p.mx.Unlock()

	seen := make(map[string]struct{}, len(p.senders))
	for _, s := range p.senders {
		seen[s] = struct{}{}
	}
	return len(seen)
}

// startPeer поднимает сервер, который отвечает body и запоминает адреса отправителей
func startPeer(t *testing.T, body []byte) (*recordingPeer, string) {
	t.Helper()

	peer := &recordingPeer{}
	s := NewServer()
	s.GET("/probe", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		peer.record(m.Sender.String())
		return NewResponse(NewBytesPayload(body), CoapCodeContent)
	})

	conn, err := newListener("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	addr := conn.LocalAddr().String()
	s.Serve(conn.(*connection).conn)
	go s.listenLoop()
	t.Cleanup(func() { _ = s.Close() })

	return peer, addr
}

func get(t *testing.T, c *Client, addr string) []byte {
	t.Helper()

	rsp, err := c.GET(fmt.Sprintf("coaps://%s/probe", addr))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if rsp.Code != CoapCodeContent {
		t.Fatalf("code %v, want %v", rsp.Code, CoapCodeContent)
	}
	return rsp.Body
}

func TestClientIdleConns_ReusesSocketAndSession(t *testing.T) {
	peer, addr := startPeer(t, []byte("ok"))

	c := NewClient(WithIdleConns(4))
	t.Cleanup(func() { _ = c.Close() })

	before := MetricSuccessfulHandhshakes.Val()
	for range 5 {
		if got := get(t, c, addr); string(got) != "ok" {
			t.Fatalf("payload %q", got)
		}
	}
	handshakes := MetricSuccessfulHandhshakes.Val() - before

	if n := peer.distinctSenders(); n != 1 {
		t.Fatalf("5 requests came from %d source ports, want 1 reused socket", n)
	}
	// один handshake на сокет: по разу на каждой стороне
	if handshakes > 2 {
		t.Fatalf("%d handshakes for 5 requests over one socket, want the session reused", handshakes)
	}
}

func TestClient_DefaultKeepsSocketPerRequest(t *testing.T) {
	peer, addr := startPeer(t, []byte("ok"))

	c := NewClient()
	for range 3 {
		get(t, c, addr)
	}

	if n := peer.distinctSenders(); n != 3 {
		t.Fatalf("got %d distinct source ports, want 3: pooling must stay opt-in", n)
	}
}

// Большой ответ идёт block2-передачей, после которой в сокете остаётся хвост пакетов.
// Если его не вычитать перед возвратом в пул, он достанется следующему запросу.
func TestClientIdleConns_Block2ResponseTwice(t *testing.T) {
	want := bytes.Repeat([]byte("0123456789"), 700) // ~7 КБ, 7 блоков
	peer, addr := startPeer(t, want)

	c := NewClient(WithIdleConns(2))
	t.Cleanup(func() { _ = c.Close() })

	for i := range 2 {
		if got := get(t, c, addr); !bytes.Equal(got, want) {
			t.Fatalf("request %d: got %d bytes, want %d", i+1, len(got), len(want))
		}
	}
	if n := peer.distinctSenders(); n != 1 {
		t.Fatalf("block2 requests came from %d source ports, want 1", n)
	}
}

// Параллельные запросы не должны делить один сокет: receiveMessage читает всё, что
// пришло, и отдал бы ответ не тому вызывающему.
func TestClientIdleConns_ConcurrentRequestsGetOwnSockets(t *testing.T) {
	peer, addr := startPeer(t, []byte("ok"))

	c := NewClient(WithIdleConns(8))
	t.Cleanup(func() { _ = c.Close() })

	const n = 4
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rsp, err := c.GET(fmt.Sprintf("coaps://%s/probe", addr))
			if err != nil {
				errs <- err
				return
			}
			if string(rsp.Body) != "ok" {
				errs <- fmt.Errorf("payload %q", rsp.Body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent GET: %v", err)
	}

	if got := peer.distinctSenders(); got != n {
		t.Fatalf("%d concurrent requests shared %d sockets, want %d", n, got, n)
	}
}

func TestClientClose_ReleasesIdleConns(t *testing.T) {
	peer, addr := startPeer(t, []byte("ok"))

	c := NewClient(WithIdleConns(4))
	get(t, c, addr)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// клиент остаётся рабочим, но сокет уже другой
	get(t, c, addr)
	t.Cleanup(func() { _ = c.Close() })

	if n := peer.distinctSenders(); n != 2 {
		t.Fatalf("got %d distinct source ports, want 2 after Close", n)
	}
}

// Сокет, пролежавший дольше idleConnTTL, переиспользовать нельзя
func TestConnPool_DropsExpiredIdleConns(t *testing.T) {
	_, addr := startPeer(t, []byte("ok"))

	p := newConnpool(false, 4)
	conn, err := p.Dial(addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	p.Release(addr, conn, true)

	p.mx.Lock()
	p.idle[addr][0].parkedAt = time.Now().Add(-idleConnTTL - time.Second)
	p.mx.Unlock()

	if got := p.take(addr); got != nil {
		t.Fatal("expired socket was handed out")
	}
	p.mx.Lock()
	left := len(p.idle[addr])
	p.mx.Unlock()
	if left != 0 {
		t.Fatalf("%d expired sockets left in the pool", left)
	}
}

// Припаркованные сокеты держат токены balance, поэтому у пула есть общий потолок:
// иначе клиент со многими адресами выбрал бы весь NumberConnections парковкой и
// следующий Dial встал бы навсегда.
func TestConnPool_IdleBudgetCapsParkedConns(t *testing.T) {
	_, addr := startPeer(t, []byte("ok"))

	p := newConnpool(false, 64)        // на адрес - сколько угодно
	p.balance = make(chan struct{}, 8) // общий бюджет парковки = 4
	defer p.Close()

	conns := make([]Transport, 0, 8)
	for range 8 {
		conn, err := p.Dial(addr)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		p.Release(addr, conn, true)
	}

	p.mx.Lock()
	total := p.idleTotal
	p.mx.Unlock()
	if total != p.idleBudget() {
		t.Fatalf("parked %d sockets, budget is %d", total, p.idleBudget())
	}
}
