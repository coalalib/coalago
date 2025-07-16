package coalago

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

var tcpConnMap sync.Map

type tcpConnection struct {
	conn *net.TCPConn
}

func (c *tcpConnection) Close() error {
	return c.conn.Close()
}

func (c *tcpConnection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *tcpConnection) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *tcpConnection) Read(buff []byte) (int, error) {
	return ReadTcpFrame(c.conn, buff)
}

// Listen для TCP просто Read (нет адреса отправителя)
func (c *tcpConnection) Listen(buff []byte) (int, net.Addr, error) {
	n, err := ReadTcpFrame(c.conn, buff)
	return n, c.conn.RemoteAddr(), err
}

func (c *tcpConnection) Write(buf []byte) (int, error) {
	return WriteTcpFrame(c.conn, buf)
}

// WriteTo для TCP игнорирует addr (point-to-point)
func (c *tcpConnection) WriteTo(buf []byte, addr string) (int, error) {
	return WriteTcpFrame(c.conn, buf)
}

func (c *tcpConnection) SetReadDeadline() {
	c.conn.SetReadDeadline(time.Now().Add(timeWait))
}

func (c *tcpConnection) SetReadDeadlineSec(timeout time.Duration) {
	c.conn.SetReadDeadline(time.Now().Add(timeout))
}

func (c *tcpConnection) SetUDPRecvBuf(size int) int {
	// Not applicable for TCP, just return the input size
	return size
}

// --- RFC 8323 framing helpers ---
func encodeLength(length int) []byte {
	switch {
	case length < 1<<7:
		return []byte{byte(length)}
	case length < 1<<14:
		b := make([]byte, 2)
		b[0] = 0x80 | byte(length>>8)
		b[1] = byte(length)
		return b
	case length < 1<<21:
		b := make([]byte, 3)
		b[0] = 0xC0 | byte(length>>16)
		b[1] = byte(length >> 8)
		b[2] = byte(length)
		return b
	default:
		b := make([]byte, 5)
		b[0] = 0xE0
		binary.BigEndian.PutUint32(b[1:], uint32(length))
		return b
	}
}

func decodeLength(r io.Reader) (int, error) {
	var first [1]byte
	_, err := io.ReadFull(r, first[:])
	if err != nil {
		return 0, err
	}
	switch {
	case first[0]&0x80 == 0:
		return int(first[0]), nil
	case first[0]&0xC0 == 0x80:
		var b [1]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(first[0]&0x3F)<<8 | int(b[0]), nil
	case first[0]&0xE0 == 0xC0:
		var b [2]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(first[0]&0x1F)<<16 | int(b[0])<<8 | int(b[1]), nil
	case first[0] == 0xE0:
		var b [4]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(binary.BigEndian.Uint32(b[:])), nil
	default:
		return 0, io.ErrUnexpectedEOF
	}
}

func ReadTcpFrame(r io.Reader, buff []byte) (int, error) {
	length, err := decodeLength(r)
	if err != nil {
		return 0, err
	}
	if length > len(buff) {
		return 0, io.ErrShortBuffer
	}

	_, err = io.ReadFull(r, buff[:length])
	if err != nil {
		return 0, err
	}
	return length, nil
}

func WriteTcpFrame(w io.Writer, data []byte) (int, error) {
	prefix := encodeLength(len(data))
	n1, err := w.Write(prefix)
	if err != nil {
		return n1, err
	}
	n2, err := w.Write(data)
	return n1 + n2, err
}

// TCP dialer
func newDialerTCP(addr string) (Transport, error) {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTCP("tcp4", nil, a)
	if err != nil {
		return nil, err
	}
	return &tcpConnection{conn: conn}, nil
}

// TCP listener
func newListenerTCP(addr string) (Transport, error) {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}
	ln, err := net.ListenTCP("tcp4", a)
	if err != nil {
		return nil, err
	}
	return &tcpListenerWrapper{ln: ln}, nil
}

type tcpListenerWrapper struct {
	ln *net.TCPListener
}

func (l *tcpListenerWrapper) Close() error {
	return l.ln.Close()
}

func (l *tcpListenerWrapper) RemoteAddr() net.Addr {
	return l.ln.Addr()
}

func (l *tcpListenerWrapper) LocalAddr() net.Addr {
	return l.ln.Addr()
}

func (l *tcpListenerWrapper) Read(buff []byte) (int, error) {
	conn, err := l.ln.AcceptTCP()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return ReadTcpFrame(conn, buff)
}

func (l *tcpListenerWrapper) Listen(buff []byte) (int, net.Addr, error) {
	conn, err := l.ln.AcceptTCP()
	if err != nil {
		return 0, nil, err
	}

	n, err := ReadTcpFrame(conn, buff)
	return n, conn.RemoteAddr(), err
}

func (l *tcpListenerWrapper) Write(buf []byte) (int, error) {
	return 0, ErrNotImplemented // not used for listener
}

func (l *tcpListenerWrapper) WriteTo(buf []byte, addr string) (int, error) {
	return 0, ErrNotImplemented // not used for listener
}

func (l *tcpListenerWrapper) SetReadDeadline() {
	l.ln.SetDeadline(time.Now().Add(timeWait))
}

func (l *tcpListenerWrapper) SetReadDeadlineSec(timeout time.Duration) {
	l.ln.SetDeadline(time.Now().Add(timeout))
}

func (l *tcpListenerWrapper) SetUDPRecvBuf(size int) int {
	// Not applicable for TCP, just return the input size
	return size
}
