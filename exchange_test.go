package coalago

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerKeyMatchesStringEquality(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	randAddr := func() *net.UDPAddr {
		ips := []net.IP{
			net.IPv4(10, 0, 0, byte(r.IntN(3))).To4(),
			net.IPv4(10, 0, 0, byte(r.IntN(3))), // тот же IPv4 в 16-байтовой форме
			net.ParseIP(fmt.Sprintf("2001:db8::%d", r.IntN(3))),
			net.ParseIP(fmt.Sprintf("fe80::%d", r.IntN(3))),
		}
		zones := []string{"", "", "eth0", "eth1"}
		return &net.UDPAddr{IP: ips[r.IntN(len(ips))], Port: 5683 + r.IntN(2), Zone: zones[r.IntN(len(zones))]}
	}
	for range 20000 {
		a, b := randAddr(), randAddr()
		if (peerKeyOf(a) == peerKeyOf(b)) != (a.String() == b.String()) {
			t.Fatalf("%v vs %v: key equality differs from String() equality", a, b)
		}
		if a.Zone == "" && peerKeyString(a.String()) != peerKeyOf(a) {
			t.Fatalf("peerKeyString(%q) differs from peerKeyOf", a.String())
		}
	}
	tcp := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5683}
	udp := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5683}
	if peerKeyOf(tcp) != peerKeyOf(udp) {
		t.Fatal("TCP and UDP address with the same ip:port must give the same key, as their String() does")
	}
	if peerKeyString("localhost:5683") == peerKeyString("localhost:5684") {
		t.Fatal("non-IP addresses must not collide")
	}
}

// testServer — сервер на транспорте в памяти, ответы приходят в канал.
type testServer struct {
	s         *Server
	mt        *memTransport
	responses chan []byte
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{s: NewServer(), responses: make(chan []byte, 1024)}
	ts.mt = startMemServer(t, ts.s)
	ts.mt.onWrite = func(buf []byte) { ts.responses <- append([]byte(nil), buf...) }
	return ts
}

func (ts *testServer) send(t *testing.T, m *CoAPMessage, from *net.UDPAddr) {
	t.Helper()
	data, err := Serialize(m)
	if err != nil {
		t.Fatal(err)
	}
	ts.mt.in <- memDatagram{b: data, from: from}
}

func (ts *testServer) expectResponse(t *testing.T, token []byte) *CoAPMessage {
	t.Helper()
	select {
	case buf := <-ts.responses:
		m, err := Deserialize(buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(m.Token, token) {
			t.Fatalf("response token %x, want %x", m.Token, token)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("no response for token %x", token)
		return nil
	}
}

func (ts *testServer) expectNoResponse(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case buf := <-ts.responses:
		m, _ := Deserialize(buf)
		t.Fatalf("unexpected response %v", m.ToReadableString())
	case <-time.After(within):
	}
}

func uniqueToken() []byte {
	return binary.BigEndian.AppendUint64(nil, benchSeq.Add(1)|1<<63)
}

func request(path string, token []byte) *CoAPMessage {
	m := NewCoAPMessage(CON, POST)
	m.Token = token
	m.SetURIPath(path)
	return m
}

func TestServerDropsRetransmitWhileHandlerRuns(t *testing.T) {
	ts := newTestServer(t)
	var calls atomic.Int32
	started, release := make(chan struct{}, 8), make(chan struct{})
	ts.s.POST("/slow", func(*CoAPMessage) *CoAPResourceHandlerResult {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return NewResponse(NewStringPayload("ok"), CoapCodeChanged)
	})

	peer := &net.UDPAddr{IP: net.IPv4(10, 1, 1, 1).To4(), Port: 40001}
	token := uniqueToken()
	ts.send(t, request("/slow", token), peer)
	<-started
	ts.send(t, request("/slow", token), peer) // ретрансмит во время работы обработчика
	ts.expectNoResponse(t, 100*time.Millisecond)
	close(release)

	ts.expectResponse(t, token)
	ts.expectNoResponse(t, 100*time.Millisecond)

	// Запоздалый ретрансмит после ответа тоже отбрасывается.
	ts.send(t, request("/slow", token), peer)
	ts.expectNoResponse(t, 100*time.Millisecond)

	// Тот же токен от другого пира — другой обмен.
	ts.send(t, request("/slow", token), &net.UDPAddr{IP: net.IPv4(10, 1, 1, 2).To4(), Port: 40001})
	ts.expectResponse(t, token)

	if n := calls.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want 2", n)
	}
}

// Одновременные копии запроса: обработчик ровно один раз. Прежнее состояние обмена
// создавалось неатомарно (Load, затем Store), и две копии могли запустить обработчик дважды.
func TestServerRunsHandlerOnceForConcurrentDuplicates(t *testing.T) {
	ts := newTestServer(t)
	var calls atomic.Int32
	ts.s.POST("/once", func(*CoAPMessage) *CoAPResourceHandlerResult {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return NewResponse(NewStringPayload("ok"), CoapCodeChanged)
	})

	peer := &net.UDPAddr{IP: net.IPv4(10, 1, 2, 1).To4(), Port: 40002}
	for range 50 {
		token := uniqueToken()
		data, _ := Serialize(request("/once", token))
		for range 16 {
			ts.mt.in <- memDatagram{b: data, from: peer}
		}
		ts.expectResponse(t, token)
	}
	ts.expectNoResponse(t, 100*time.Millisecond)
	if n := calls.Load(); n != 50 {
		t.Fatalf("handler ran %d times for 50 requests", n)
	}
}

// Медленный обработчик не задерживает остальные запросы: свободной горутины нет —
// запускается новая.
func TestServerSlowHandlerDoesNotBlockOthers(t *testing.T) {
	ts := newTestServer(t)
	release := make(chan struct{})
	defer close(release)
	var slowStarted sync.WaitGroup
	slowStarted.Add(64)
	ts.s.POST("/slow", func(*CoAPMessage) *CoAPResourceHandlerResult {
		slowStarted.Done()
		<-release
		return nil
	})
	ts.s.POST("/fast", func(*CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewStringPayload("fast"), CoapCodeChanged)
	})

	peer := &net.UDPAddr{IP: net.IPv4(10, 1, 3, 1).To4(), Port: 40003}
	for range 64 {
		ts.send(t, request("/slow", uniqueToken()), peer)
	}
	slowStarted.Wait()

	token := uniqueToken()
	ts.send(t, request("/fast", token), peer)
	if got := ts.expectResponse(t, token).Payload.String(); got != "fast" {
		t.Fatalf("payload %q", got)
	}
}

// Приём, вставший на записи в сокет, держит только замок своего обмена: остальные
// обмены, включая coaps, обрабатываются. С общими полосами замков сотни зависших
// обменов занимали бы все полосы и останавливали приём всех серверов процесса.
func TestBlockedAcceptDoesNotStallOtherExchanges(t *testing.T) {
	s := NewServer()
	s.POST("/a", aliveHandler)
	mt := startMemServer(t, s)

	unblock := make(chan struct{})
	defer close(unblock)
	var blocked atomic.Int32
	responses := make(chan []byte, 16)
	mt.onWrite = func(buf []byte) {
		if len(buf) > 1 && buf[1] == byte(CoapCodeUnauthorized) {
			blocked.Add(1)
			<-unblock // SessionNotFound пиру без сессии: запись «висит»
			return
		}
		responses <- append([]byte(nil), buf...)
	}

	// Пир без сессии: каждый его coaps-запрос упирается в зависшую запись ответа.
	noSession := &net.UDPAddr{IP: net.IPv4(10, 2, 0, 1).To4(), Port: 40100}
	for range 300 {
		m := newAliveRequest()
		m.Token = uniqueToken()
		m.SetSchemeCOAPS()
		data, _ := Serialize(m)
		mt.in <- memDatagram{b: data, from: noSession}
	}
	deadline := time.Now().Add(5 * time.Second)
	for blocked.Load() < 300 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d writes blocked", blocked.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Пир с сессией получает ответ.
	srvSession, cliSession := sessionPair(t)
	peer := &net.UDPAddr{IP: net.IPv4(10, 2, 0, 2).To4(), Port: 40101}
	local := mt.LocalAddr().String()
	s.sessions.Set(local, peer.String(), "", srvSession)
	req := newAliveRequest()
	req.Token = uniqueToken()
	req.SetSchemeCOAPS()
	if err := encrypt(req, local, cliSession.AEAD); err != nil {
		t.Fatal(err)
	}
	data, _ := Serialize(req)
	mt.in <- memDatagram{b: data, from: peer}

	select {
	case buf := <-responses:
		rsp, err := Deserialize(buf)
		if err != nil || !bytes.Equal(rsp.Token, req.Token) {
			t.Fatalf("unexpected response %x: %v", buf, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coaps exchange with a session stalled behind blocked exchanges")
	}
}

func TestResourceLookup(t *testing.T) {
	s := NewServer()
	handler := func(name string) CoAPResourceHandler {
		return func(*CoAPMessage) *CoAPResourceHandlerResult {
			return NewResponse(NewStringPayload(name), CoapCodeContent)
		}
	}
	s.GET("/a/b/", handler("get-ab"))
	s.POST("a/b", handler("post-ab"))
	s.DELETE("*", handler("delete-any"))

	lookup := func(method CoapCode, path string) string {
		m := NewCoAPMessage(CON, method)
		m.SetURIPath(path)
		res := s.resourceFor(m)
		if res == nil {
			return ""
		}
		return res.Handler(m).Payload.String()
	}
	for _, c := range []struct {
		method CoapCode
		path   string
		want   string
	}{
		{GET, "/a/b", "get-ab"},
		{GET, "a/b/", "get-ab"},
		{POST, "/a/b", "post-ab"},
		{PUT, "/a/b", ""},
		{GET, "/a", ""},
		{DELETE, "/anything/at/all", "delete-any"},
	} {
		if got := lookup(c.method, c.path); got != c.want {
			t.Fatalf("%v %s -> %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestProxyTableExpiry(t *testing.T) {
	pt := newProxyTable(50 * time.Millisecond)
	k := exKeyOf(peerKeyString("10.0.0.1:5683"), []byte("tok"))
	note := &proxyNote{}
	if _, ok := pt.get(k); ok {
		t.Fatal("empty table returned a route")
	}
	pt.set(k, note)
	if got, ok := pt.get(exKeyOf(peerKeyOf(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5683}), []byte("tok"))); !ok || got != note {
		t.Fatal("route set by host string is not found by the responder's UDP address")
	}
	// Использование продлевает срок.
	for range 4 {
		time.Sleep(20 * time.Millisecond)
		if _, ok := pt.get(k); !ok {
			t.Fatal("route expired while in use")
		}
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := pt.get(k); ok {
		t.Fatal("route did not expire")
	}
	if n := pt.itemCount(); n != 0 {
		t.Fatalf("itemCount %d after expiry", n)
	}
}

// Server.Send с портом сервера: ответ приходит через ожидание в bq; для coaps — ещё и
// handshake с сессией на стороне отправителя.
func TestServerSendRoundTrip(t *testing.T) {
	for _, scheme := range []string{"coap", "coaps"} {
		t.Run(scheme, func(t *testing.T) {
			peer := NewServer(WithPrivateKey([]byte("peer-key")))
			peer.GET("/ping", func(m *CoAPMessage) *CoAPResourceHandlerResult {
				return NewResponse(NewStringPayload("pong:"+m.GetURIQuery("n")), CoapCodeContent)
			})
			peerAddr := startUDPServer(t, peer)

			sender := NewServer(WithPrivateKey([]byte("sender-key")))
			startUDPServer(t, sender)

			for i := range 5 {
				m := NewCoAPMessage(CON, GET)
				if scheme == "coaps" {
					m.SetSchemeCOAPS()
				}
				m.SetURIPath("/ping")
				m.SetURIQuery("n", fmt.Sprint(i))
				rsp, err := sender.Send(m, peerAddr, WithRetries(2))
				if err != nil {
					t.Fatalf("Send %d: %v", i, err)
				}
				if got, want := rsp.Payload.String(), fmt.Sprintf("pong:%d", i); got != want {
					t.Fatalf("response %q, want %q", got, want)
				}
			}
		})
	}
}

// Параллельные большие запросы: блоки каждого обмена собираются в одно состояние.
// Прежнее неатомарное создание состояния теряло блоки, и передача висла навсегда.
func TestBlockwiseParallelUploads(t *testing.T) {
	s := NewServer()
	s.POST("/echo", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewBytesPayload(m.Payload.Bytes()), CoapCodeChanged)
	})
	uri := fmt.Sprintf("coap://%s/echo", startUDPServer(t, s))

	done := make(chan struct{})
	watchdog := time.AfterFunc(60*time.Second, func() { panic("block transfer hung") })
	defer watchdog.Stop()

	var wg sync.WaitGroup
	for w := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewClient()
			for i := range 5 {
				body := bytes.Repeat([]byte{byte(w), byte(i)}, 16<<10)
				rsp, err := c.POST(body, uri)
				if err != nil {
					t.Errorf("worker %d upload %d: %v", w, i, err)
					return
				}
				if !bytes.Equal(rsp.Body, body) {
					t.Errorf("worker %d upload %d: echo differs (%d bytes)", w, i, len(rsp.Body))
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	<-done
}

// TCP-сервер читает кадры в один буфер: сообщение, ещё обрабатываемое в другой
// горутине, не должно видеть данные следующего кадра.
func TestTCPServerMessagesDoNotAliasReadBuffer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer()
	s.POST("/echo", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		// Пока обработчик спит, следующие кадры читаются в тот же буфер. Ответы разнесены
		// по времени: WriteTcpFrame пишет префикс и данные двумя Write, и одновременные
		// ответы в одно соединение перемешались бы (отдельная задача в TODO).
		id, _ := strconv.Atoi(m.GetURIQuery("id"))
		time.Sleep(time.Duration(20+15*id) * time.Millisecond)
		return NewResponse(NewBytesPayload(append([]byte(m.GetURIQuery("id")+":"), m.Payload.Bytes()...)), CoapCodeChanged)
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.HandleTCPConn(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	want := map[string]string{}
	for i := range 8 {
		m := NewCoAPMessage(CON, POST)
		m.Token = uniqueToken()
		m.SetURIPath("/echo")
		id := fmt.Sprint(i)
		m.SetURIQuery("id", id)
		m.Payload = NewStringPayload(fmt.Sprintf("body-%d-%s", i, bytes.Repeat([]byte{'x'}, i*7)))
		want[string(m.Token)] = id + ":" + m.Payload.String()
		data, _ := Serialize(m)
		if _, err := WriteTcpFrame(conn, data); err != nil {
			t.Fatal(err)
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	for range want {
		n, err := ReadTcpFrame(conn, buf)
		if err != nil {
			t.Fatal(err)
		}
		rsp, err := Deserialize(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		if got := rsp.Payload.String(); got != want[string(rsp.Token)] {
			t.Fatalf("token %x: response %q, want %q", rsp.Token, got, want[string(rsp.Token)])
		}
	}
}
