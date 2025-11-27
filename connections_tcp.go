package coalago

import (
	"encoding/binary"
	"io"
	"net"
	"time"
)

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
func (c *tcpConnection) WriteTo(buf []byte, _ string) (int, error) {
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
	case length <= 12:
		return []byte{byte(length)}
	case length <= 255+13:
		return []byte{13, byte(length - 13)}
	case length <= 65535+269:
		b := make([]byte, 3)
		b[0] = 14
		binary.BigEndian.PutUint16(b[1:], uint16(length-269))
		return b
	default:
		b := make([]byte, 5)
		b[0] = 15
		binary.BigEndian.PutUint32(b[1:], uint32(length-65805))
		return b
	}
}

func decodeLength(r io.Reader) (int, error) {
	var first [1]byte
	_, err := io.ReadFull(r, first[:])
	if err != nil {
		return 0, err
	}

	length := int(first[0] & 0x0F) // Получаем 4-битное поле Length

	switch length {
	case 13:
		var b [1]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(b[0]) + 13, nil
	case 14:
		var b [2]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(binary.BigEndian.Uint16(b[:])) + 269, nil
	case 15:
		var b [4]byte
		_, err := io.ReadFull(r, b[:])
		if err != nil {
			return 0, err
		}
		return int(binary.BigEndian.Uint32(b[:])) + 65805, nil
	default:
		return length, nil
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
	conn, err := net.DialTCP("tcp", nil, a)
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
	ln, err := net.ListenTCP("tcp", a)
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
