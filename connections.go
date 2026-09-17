package coalago

import (
	"bytes"
	"net"
	"sync"
	"time"
)

var NumberConnections = 1024
var globalPoolConnections = newConnpool(false, 0)

const (
	// idleConnTTL - сколько простаивающий сокет ждёт следующего запроса. Сокет, переживший
	// SESSIONS_POOL_EXPIRATION, всё ещё полезен: handshake по нему пройдёт заново, а вот
	// новый сокет стоил бы пиру ещё одной записи в таблице сессий.
	idleConnTTL = 5 * time.Minute
	// maxDrainPackets - потолок вычитывания хвоста обмена, чтобы не крутиться на потоке мусора
	maxDrainPackets = 64
)

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

// drain вычитывает всё, что осталось в сокете от завершённого обмена: дубли ACK и
// опоздавшие ретрансмиты. Без этого хвост достаётся следующему запросу по тому же
// сокету - по токену receiveMessage его отбросит, но пакет, зашифрованный уже
// смененной сессией, не расшифруется и уронит чужой запрос.
func (c *connection) drain() {
	if err := c.conn.SetReadDeadline(time.Now()); err != nil {
		return
	}
	buf := make([]byte, MTU+1)
	for range maxDrainPackets {
		if _, err := c.conn.Read(buf); err != nil {
			break
		}
	}
	c.conn.SetReadDeadline(time.Time{})
}

func (c *connection) Close() error {
	err := c.conn.Close()
	// Если канал задан, то освобождаем ресурс. Неблокирующее чтение: у listener-соединений
	// канал создается пустым (токен кладет только newDialer), и блокирующее чтение
	// подвешивало бы Close (а с ним и Server.Refresh) навсегда.
	if c.end != nil {
		select {
		case <-c.end:
		default:
		}
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

// resolvedAddrs кэширует разобранные ip:port адреса: WriteTo вызывается на каждый блок
// ARQ-передачи (тысячи пакетов на одно большое тело), и повторный ResolveUDPAddr одного
// и того же адреса — лишние парсинг и аллокации в горячем пути. TTL-кэш, а не карта:
// адреса NAT-клиентов уникальны и без вытеснения память росла бы неограниченно.
var resolvedAddrs = newShardedCache(SESSIONS_POOL_EXPIRATION)

func resolveUDPAddrCached(addr string) (*net.UDPAddr, error) {
	if v, ok := resolvedAddrs.Get(addr); ok {
		return v.(*net.UDPAddr), nil
	}
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	resolvedAddrs.Set(addr, a)
	return a, nil
}

func (c *connection) WriteTo(buf []byte, addr string) (int, error) {
	a, err := resolveUDPAddrCached(addr)
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

type idleConn struct {
	conn     *connection
	parkedAt time.Time
}

type connpool struct {
	balance chan struct{}
	useTCP  bool
	// maxIdle - сколько простаивающих сокетов на адрес пира держать для переиспользования.
	// 0 (по умолчанию) - сокет закрывается после каждого обмена, как раньше.
	maxIdle int

	mx        sync.Mutex
	idle      map[string][]idleConn
	idleTotal int
}

// idleBudget - потолок припаркованных сокетов на весь пул. Сокет в пуле держит токен
// balance (он занят с DialUDP до Close), поэтому без общего потолка клиент, работающий
// с сотнями адресов, выбрал бы весь NumberConnections парковкой, и следующий Dial встал
// бы навсегда. Половина бюджета остаётся под активные обмены.
func (c *connpool) idleBudget() int {
	return cap(c.balance) / 2
}

func newConnpool(useTCP bool, maxIdle int) *connpool {
	return &connpool{
		balance: make(chan struct{}, NumberConnections),
		useTCP:  useTCP,
		maxIdle: maxIdle,
		idle:    make(map[string][]idleConn),
	}
}

func (c *connpool) Dial(addr string) (Transport, error) {
	if c.useTCP {
		return newDialerTCP(addr)
	}
	if conn := c.take(addr); conn != nil {
		return conn, nil
	}
	return newDialer(c.balance, addr)
}

// Release возвращает сокет в пул или закрывает его. reusable=false для любого обмена,
// закончившегося ошибкой: состояние такого сокета неизвестно, а сессия на той стороне
// могла уже смениться.
func (c *connpool) Release(addr string, conn Transport, reusable bool) {
	udp, ok := conn.(*connection)
	if !ok || !reusable || c.maxIdle <= 0 {
		conn.Close()
		return
	}

	udp.drain()

	c.mx.Lock()
	c.dropExpiredLocked(addr)
	if len(c.idle[addr]) >= c.maxIdle || c.idleTotal >= c.idleBudget() {
		c.mx.Unlock()
		conn.Close()
		return
	}
	c.idle[addr] = append(c.idle[addr], idleConn{conn: udp, parkedAt: time.Now()})
	c.idleTotal++
	c.mx.Unlock()
}

// take забирает свободный сокет к addr, если он есть. LIFO: у самого свежего сокета
// выше шанс, что сессия на той стороне ещё жива. Выданный сокет из пула убирается -
// два запроса одновременно им пользоваться не могут, иначе receiveMessage отдал бы
// ответ не тому вызывающему.
func (c *connpool) take(addr string) Transport {
	c.mx.Lock()
	defer c.mx.Unlock()

	c.dropExpiredLocked(addr)
	conns := c.idle[addr]
	if len(conns) == 0 {
		return nil
	}

	last := conns[len(conns)-1]
	if len(conns) == 1 {
		delete(c.idle, addr)
	} else {
		c.idle[addr] = conns[:len(conns)-1]
	}
	c.idleTotal--
	return last.conn
}

func (c *connpool) dropExpiredLocked(addr string) {
	conns := c.idle[addr]
	alive := conns[:0]
	for _, ic := range conns {
		if time.Since(ic.parkedAt) > idleConnTTL {
			ic.conn.Close()
			c.idleTotal--
			continue
		}
		alive = append(alive, ic)
	}
	if len(alive) == 0 {
		delete(c.idle, addr)
		return
	}
	c.idle[addr] = alive
}

// Close закрывает простаивающие сокеты. Пул остаётся рабочим: следующий Dial откроет
// сокет заново.
func (c *connpool) Close() {
	c.mx.Lock()
	idle := c.idle
	c.idle = make(map[string][]idleConn)
	c.idleTotal = 0
	c.mx.Unlock()

	for _, conns := range idle {
		for _, ic := range conns {
			ic.conn.Close()
		}
	}
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
			if err == ErrChecksumMismatch {
				continue
			}
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
