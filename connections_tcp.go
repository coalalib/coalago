package coalago

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
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
	wrp, err := ReadTCPFrame(c.conn, buff)
	return int(wrp.size), err
}

// Listen для TCP просто Read (нет адреса отправителя)
func (c *tcpConnection) Listen(buff []byte) (int, net.Addr, error) {
	wrp, err := ReadTCPFrame(c.conn, buff)
	return int(wrp.size), c.conn.RemoteAddr(), err
}

func (c *tcpConnection) Write(buf []byte) (int, error) {
	return c.conn.Write(buf)
}

// WriteTo для TCP игнорирует addr (point-to-point)
func (c *tcpConnection) WriteTo(buf []byte, _ string) (int, error) {
	return c.conn.Write(buf)
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

type tcpWrapper struct {
	ip   net.IP
	port uint16
	size uint16
}

func ReadTCPFrame(r io.Reader, buff []byte) (tcpWrapper, error) {
	wrp := tcpWrapper{}
	var first [1]byte
	_, err := io.ReadFull(r, first[:])
	if err != nil {
		return wrp, err
	}

	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!!first")
	if first[0] != 0x4D {
		return wrp, fmt.Errorf("invalid frame")
	}

	var meta [4 + 2 + 2]byte
	_, err = io.ReadFull(r, meta[:])
	if err != nil {
		return wrp, err
	}

	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!! meta")
	wrp.ip = net.IP(meta[:4])
	wrp.port = binary.BigEndian.Uint16(meta[4:6])
	wrp.size = binary.BigEndian.Uint16(meta[6:])

	if int(wrp.size) > len(buff) {
		return wrp, io.ErrShortBuffer
	}

	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!! meta", wrp.ip, wrp.port, wrp.size, len(buff))

	_, err = io.ReadFull(r, buff[:wrp.size])
	if err != nil {
		return wrp, err
	}

	return wrp, nil
}

func encodeTCPFrame(data []byte, sender net.Addr) ([]byte, error) {
	ip, port, err := getIpPort(sender)
	if err != nil {
		return nil, err
	}

	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!! encodeTCPFrame", ip, port)

	buf := make([]byte, 1+4+2+2)
	buf[0] = 0x4D
	copy(buf[1:5], ip.To4())
	binary.BigEndian.PutUint16(buf[5:7], uint16(port))
	binary.BigEndian.PutUint16(buf[7:9], uint16(len(data)))
	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!! encodeTCPFrame", len(data), len(buf), len(buf)+len(data))
	//fmt.Println("!!!!!!!!!!!!!!!!!!!!!! encodeTCPFramebuf", buf[1:5])
	return append(buf, data...), nil
}

func getIpPort(sender net.Addr) (net.IP, uint16, error) {
	if sender == nil {
		return nil, 0, nil
	}

	ip, port, err := net.SplitHostPort(sender.String())
	if err != nil {
		return nil, 0, err
	}

	portInt, err := strconv.Atoi(port)
	if err != nil {
		return nil, 0, err
	}

	return net.ParseIP(ip), uint16(portInt), nil
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

	wrp, err := ReadTCPFrame(conn, buff)
	if err != nil {
		return 0, err
	}

	return int(wrp.size), nil
}

func (l *tcpListenerWrapper) Listen(buff []byte) (int, net.Addr, error) {
	conn, err := l.ln.AcceptTCP()
	if err != nil {
		return 0, nil, err
	}

	wrp, err := ReadTCPFrame(conn, buff)
	if err != nil {
		return 0, nil, err
	}

	return int(wrp.size), conn.RemoteAddr(), nil
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
