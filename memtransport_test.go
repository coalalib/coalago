package coalago

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coalalib/coalago/session"
)

type memDatagram struct {
	b    []byte
	from *net.UDPAddr
}

// memTransport — серверный сокет в памяти: Listen отдаёт датаграммы из канала, записи
// уходят в onWrite.
type memTransport struct {
	in      chan memDatagram
	done    chan struct{}
	once    sync.Once
	local   *net.UDPAddr
	onWrite func(buf []byte)
}

func newMemTransport() *memTransport {
	return &memTransport{
		in:    make(chan memDatagram, 1024),
		done:  make(chan struct{}),
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1).To4(), Port: 5683},
	}
}

func (t *memTransport) Listen(buf []byte) (int, net.Addr, error) {
	select {
	case d := <-t.in:
		return copy(buf, d.b), d.from, nil
	case <-t.done:
		return 0, nil, net.ErrClosed
	}
}

func (t *memTransport) WriteTo(buf []byte, _ string) (int, error) {
	t.onWrite(buf)
	return len(buf), nil
}

// listenUDPAddr и writeToAddr — те же быстрые пути, что у UDP-сокета (*connection).
// Адрес отправителя отдаётся готовым: аллокацию адреса при чтении pipeline не считает
// ни до, ни после правок, её видно в BenchmarkServerAliveUDP.
func (t *memTransport) listenUDPAddr(buf []byte) (int, *net.UDPAddr, error) {
	select {
	case d := <-t.in:
		return copy(buf, d.b), d.from, nil
	case <-t.done:
		return 0, nil, net.ErrClosed
	}
}

func (t *memTransport) writeToAddr(buf []byte, _ net.Addr) (int, error) {
	t.onWrite(buf)
	return len(buf), nil
}

func (t *memTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

func (t *memTransport) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (t *memTransport) Write(buf []byte) (int, error)    { return len(buf), nil }
func (t *memTransport) RemoteAddr() net.Addr             { return nil }
func (t *memTransport) LocalAddr() net.Addr              { return t.local }
func (t *memTransport) SetReadDeadline()                 {}
func (t *memTransport) SetUDPRecvBuf(size int) int       { return size }
func (t *memTransport) SetReadDeadlineSec(time.Duration) {}

// startMemServer поднимает listenLoop сервера s поверх транспорта в памяти.
func startMemServer(tb testing.TB, s *Server) *memTransport {
	tb.Helper()
	mt := newMemTransport()
	s.sr = s.newServerTransport(mt)
	s.sr.privateKey = s.privatekey
	go s.listenLoop()
	tb.Cleanup(func() { _ = mt.Close() })
	return mt
}

// benchSeq — сквозной номер запроса на весь процесс: кэши дедупликации глобальные и
// переживают перезапуск бенчмарка с новым b.N, повтор пары (адрес, токен) из прошлого
// прогона сервер молча отбросил бы.
var benchSeq atomic.Uint64

// startUDPServer поднимает сервер на loopback-сокете.
func startUDPServer(tb testing.TB, s *Server) string {
	tb.Helper()
	conn, err := newListener("127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listener: %v", err)
	}
	s.Serve(conn.(*connection).conn)
	go s.listenLoop()
	tb.Cleanup(func() { _ = s.Close() })
	return conn.LocalAddr().String()
}

// sessionPair возвращает согласованные сессии сервера и клиента без handshake по сети.
func sessionPair(tb testing.TB) (srv, cli session.SecuredSession) {
	tb.Helper()
	srv, err := session.NewSecuredSession(nil)
	if err != nil {
		tb.Fatal(err)
	}
	cli, err = session.NewSecuredSession(nil)
	if err != nil {
		tb.Fatal(err)
	}
	srv.PeerPublicKey = cli.Curve.GetPublicKey()
	cli.PeerPublicKey = srv.Curve.GetPublicKey()

	sig, err := srv.GetSignature()
	if err != nil {
		tb.Fatal(err)
	}
	if err := srv.PeerVerify(sig); err != nil {
		tb.Fatal(err)
	}
	if sig, err = cli.GetSignature(); err != nil {
		tb.Fatal(err)
	}
	if err := cli.Verify(sig); err != nil {
		tb.Fatal(err)
	}
	return srv, cli
}
