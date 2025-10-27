package coalago

import (
	"bytes"
	"net"
	"time"
)

var NumberConnections = 1024
var globalPoolConnections = newConnpool(false)

type Transport interface {
	Close() error
	Listen([]byte) (int, net.Addr, error)
	Read(buff []byte) (int, error)
	Write(buf []byte) (int, error)
	WriteTo(buf []byte, addr string) (int, error)
	RemoteAddr() net.Addr
	LocalAddr() net.Addr
	SetReadDeadline()
	SetUDPRecvBuf(size int) int
	SetReadDeadlineSec(timeout time.Duration)
}

type connection struct {
	end  chan struct{}
	conn *net.UDPConn
}

func (c *connection) SetUDPRecvBuf(size int) int {
	for {
		if err := c.conn.SetReadBuffer(size); err == nil {
			break
		}
		size = size / 2
	}
	return size
}

func (c *connection) Close() error {
	err := c.conn.Close()
	// Если канал задан, то освобождаем ресурс
	if c.end != nil {
		<-c.end
	}
	return err
}

func (c *connection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *connection) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *connection) Read(buff []byte) (int, error) {
	return c.conn.Read(buff)
}

func (c *connection) Listen(buff []byte) (int, net.Addr, error) {
	return c.conn.ReadFromUDP(buff)
}

func (c *connection) Write(buf []byte) (int, error) {
	return c.conn.Write(buf)
}

func (c *connection) WriteTo(buf []byte, addr string) (int, error) {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return 0, err
	}
	return c.conn.WriteTo(buf, a)
}

func newDialer(end chan struct{}, addr string) (Transport, error) {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, a)
	if err != nil {
		return nil, err
	}
	// Токен резервируется только после успешного соединения
	end <- struct{}{}
	return &connection{conn: conn, end: end}, nil
}

func newListener(addr string) (Transport, error) {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", a)
	if err != nil {
		return nil, err
	}
	// Для listener создаём свой end, чтобы Close не блокировался
	return &connection{conn: conn, end: make(chan struct{}, 1)}, nil
}

type connpool struct {
	balance chan struct{}
	useTCP  bool
}

func newConnpool(useTCP bool) *connpool {
	return &connpool{
		balance: make(chan struct{}, NumberConnections),
		useTCP:  useTCP,
	}
}

func (c *connpool) Dial(addr string) (Transport, error) {
	if c.useTCP {
		return newDialerTCP(addr)
	}
	return newDialer(c.balance, addr)
}

func (c *connection) SetReadDeadline() {
	c.conn.SetReadDeadline(time.Now().Add(timeWait))
}

func (c *connection) SetReadDeadlineSec(timeout time.Duration) {
	c.conn.SetReadDeadline(time.Now().Add(timeout))
}

type packet struct {
	acked    bool
	attempts int
	lastSend time.Time
	message  *CoAPMessage
}

func receiveMessage(tr *transport, origMessage *CoAPMessage) (*CoAPMessage, error) {
	for {
		tr.conn.SetReadDeadlineSec(origMessage.Timeout)

		buff := make([]byte, MTU+1)
		n, err := tr.conn.Read(buff)
		origMessage.Timeout = timeWait
		if err != nil {
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				return nil, ErrMaxAttempts
			}
			return nil, err
		}
		if n > MTU {
			continue
		}

		message, err := preparationReceivingBuffer(tr, buff[:n], tr.conn.RemoteAddr(), origMessage.ProxyAddr)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(message.Token, origMessage.Token) {
			continue
		}
		return message, nil
	}
}

// NewTransport создает транспорт (UDP или TCP) по флагу useTCP
func NewTransport(addr string, useTCP bool) (Transport, error) {
	if useTCP {
		return newDialerTCP(addr)
	}
	return newDialer(make(chan struct{}, 1), addr)
}
