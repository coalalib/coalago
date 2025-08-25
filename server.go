package coalago

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

const (
	ConnectionTypeUDP = 1 << iota // 1
	ConnectionTypeTCP             // 2
)

type proxyNote struct {
	addr string
	tr   *transport
}

type Server struct {
	proxyEnable    bool
	sr             *transport
	resources      sync.Map
	privatekey     []byte
	addr           string       // сохраняем адрес для Refresh()
	connectionType uint8        // битовая маска для TCP/UDP
	proxyCache     *cache.Cache // token + addr -> proxyNote
}

func NewServer(opts ...Opt) *Server {
	options := &coalaopts{}
	for _, opt := range opts {
		opt(options)
	}

	return &Server{
		privatekey: options.privatekey,
		proxyCache: cache.New(time.Minute, time.Second), // token + addr -> proxyNote
	}
}

func (s *Server) ListenTCP(addr string) error {
	s.addr = addr
	s.connectionType |= ConnectionTypeTCP // устанавливаем бит TCP = 1
	return s.listenTCP(addr)
}

func (s *Server) Listen(addr string) error {
	s.addr = addr                         // сохраняем адрес для будущего рестарта
	s.connectionType |= ConnectionTypeUDP // устанавливаем бит UDP = 1

	var conn Transport
	var err error
	conn, err = newListener(addr)
	if err != nil {
		return err
	}

	s.sr = newtransport(conn)
	s.sr.privateKey = s.privatekey
	fmt.Printf(
		"COALA server start ADDR: %s, WS: %d, MinWS: %d, MaxWS: %d, Retransmit:%d, timeWait:%d, poolExpiration:%d\n",
		addr, DEFAULT_WINDOW_SIZE, MIN_WiNDOW_SIZE, MAX_WINDOW_SIZE, maxSendAttempts, timeWait, SESSIONS_POOL_EXPIRATION)

	s.listenLoop() // блокирующий цикл прослушивания
	return nil
}

func (s *Server) listenTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	fmt.Printf("COALA TCP server start ADDR: %s\n", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println("accept error:", err)
			continue
		}

		go s.HandleTCPConn(conn)
	}
}

func (s *Server) HandleTCPConn(conn net.Conn) {
	connStorage.SetTCP(conn.RemoteAddr().String(), conn)

	defer func() {
		conn.Close()
		connStorage.DeleteTCP(conn.RemoteAddr().String())
	}()

	tcpTr := newtransport(&tcpConnection{conn: conn.(*net.TCPConn)})

	buf := make([]byte, 65536)
	for {
		n, err := ReadTcpFrame(conn, buf)
		if err != nil {
			if err != io.EOF {
				fmt.Println("readFrame error:", err)
			}
			return
		}

		connStorage.SetTCP(conn.RemoteAddr().String(), conn)

		msg, err := Deserialize(buf[:n])
		if err != nil {
			fmt.Println("deserialize error:", err)
			continue
		}

		msg.Sender = conn.RemoteAddr()
		proxyUri := msg.GetOptionProxyURIasString()
		if proxyUri == "" {
			if v, ok := s.proxyCache.Get(msg.GetTokenString() + conn.RemoteAddr().String()); ok {
				note := v.(*proxyNote)
				s.proxyCache.SetDefault(msg.GetTokenString()+conn.RemoteAddr().String(), note)
				note.tr.conn.WriteTo(buf[:n], note.addr)
				continue
			}

			go s.processLocalState(msg, tcpTr)
			continue
		}

		go func() {
			parsedURL, err := url.Parse(proxyUri)
			if err != nil {
				fmt.Println("parse proxyUri error:", err)
				return
			}

			msg.RemoveOptions(OptionProxyScheme)
			msg.RemoveOptions(OptionProxyURI)

			if err := s.sendTo(msg, parsedURL.Host); err != nil {
				fmt.Println("send error:", err)
				return
			}

			s.proxyCache.SetDefault(msg.GetTokenString()+parsedURL.Host, &proxyNote{addr: msg.Sender.String(), tr: tcpTr})
			MetricProxySessions.Set(int64(s.proxyCache.ItemCount()))
			MetricProxySessionsRate.Inc()
		}()
	}
}

func (s *Server) Refresh() error {
	if s.addr == "" {
		return fmt.Errorf("server address not set")
	}
	// Закрываем старое соединение, если возможно
	if s.sr != nil && s.sr.conn != nil {
		if closer, ok := s.sr.conn.(interface{ Close() error }); ok {
			closer.Close()
		}
	}
	var conn Transport
	var err error
	if s.IsTCP() {
		conn, err = newListenerTCP(s.addr)
	} else {
		conn, err = newListener(s.addr)
	}
	if err != nil {
		return err
	}

	s.sr = newtransport(conn)
	s.sr.privateKey = s.privatekey

	go s.listenLoop() // перезапускаем цикл прослушивания в горутине
	fmt.Printf("server refreshed on ADDR: %s", s.addr)
	return nil
}

func (s *Server) GET(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodGet, path, handler))
}

func (s *Server) POST(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodPost, path, handler))
}

func (s *Server) PUT(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodPut, path, handler))
}

func (s *Server) DELETE(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodDelete, path, handler))
}

func (s *Server) Proxy(flag bool) {
	s.proxyEnable = flag
}

func (s *Server) SetPrivateKey(privateKey []byte) {
	s.privatekey = privateKey
}

func (s *Server) GetPrivateKey() []byte {
	return s.privatekey
}

func (s *Server) AddUDP(addr string) {
	connStorage.SetUDP(addr)
}

func (s *Server) sendTo(message *CoAPMessage, addr string) error {
	b, err := Serialize(message)
	if err != nil {
		return err
	}

	tr := s.sr
	if conn, ok := connStorage.GetTCP(addr); ok {
		tr = newtransport(&tcpConnection{conn: conn.(*net.TCPConn)})
	}

	_, err = tr.conn.WriteTo(b, addr)
	if err != nil {
		return err
	}

	return nil
}

// Send отправляет сообщение на указанный адрес и возвращает ответ
// используется вместо клиента, когда нужно отправить запрос с занятого сервером порта
func (s *Server) Send(message *CoAPMessage, addr string) (*CoAPMessage, error) {
	if err := s.sendTo(message, addr); err != nil {
		return nil, err
	}

	resolved, _ := net.ResolveUDPAddr("udp", addr)

	msg, err := bq.Read(message.GetTokenString() + resolved.String())
	if err != nil {
		return nil, err
	}

	return msg, nil
}

// Serve запускает сервер на указанном соединении (например, если нужно использовать свой UDP-сервер)
// нужно для прокси сервиса
func (s *Server) Serve(conn *net.UDPConn) {
	c := &connection{conn: conn}
	s.sr = newtransport(c)
	s.sr.privateKey = s.privatekey
}

// ServeMessage обрабатывает сообщение, как если бы оно пришло от клиента
// нужно для прокси сервиса
func (s *Server) ServeMessage(message *CoAPMessage) {
	go s.processLocalState(message, s.sr)
}

func (s *Server) processLocalState(message *CoAPMessage, tr *transport) {
	id := message.Sender.String() + message.GetTokenString()
	fnIfase, _ := StorageLocalStates.LoadOrStore(id, MakeLocalStateFn(s, tr, nil))
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("panic in handler: %v\n", r)
		}
	}()

	fnIfase.(LocalStateFn)(message)
}

func (s *Server) addResource(res *CoAPResource) {
	key := res.Path + fmt.Sprint(res.Method)
	s.resources.Store(key, res)
}

func (s *Server) getResourceForPathAndMethod(path string, method CoapMethod) *CoAPResource {
	path = strings.Trim(path, "/ ")
	if res, ok := s.resources.Load("*" + fmt.Sprint(method)); ok {
		return res.(*CoAPResource)
	}
	key := path + fmt.Sprint(method)
	if res, ok := s.resources.Load(key); ok {
		return res.(*CoAPResource)
	}
	return nil
}

func (s *Server) listenLoop() {
	semaphore := make(chan struct{}, maxParallel)

	for {
		readBuf := make([]byte, MTU+1)
		n, senderAddr, err := s.sr.conn.Listen(readBuf)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				fmt.Println("connection was closed")
				return
			}
			fmt.Printf("read error: %v\n", err)
			continue
		}

		connStorage.SetUDP(senderAddr.String())

		if n == 0 || n > MTU {
			if n > MTU {
				MetricMaxMTU.Inc()
			}
			continue
		}

		message, err := preparationReceivingBufferForStorageLocalStates(readBuf[:n], senderAddr)
		if err != nil {
			continue
		}

		if v, ok := s.proxyCache.Get(message.GetTokenString() + senderAddr.String()); ok {
			note := v.(*proxyNote)
			s.proxyCache.SetDefault(message.GetTokenString()+senderAddr.String(), note)
			note.tr.conn.WriteTo(readBuf[:n], note.addr)
			continue
		}

		semaphore <- struct{}{}

		if message.GetOptionProxyURIasString() != "" {
			go func() {
				parsedURL, err := url.Parse(message.GetOptionProxyURIasString())
				if err != nil {
					fmt.Println("parse proxyUri error:", err)
					return
				}

				message.RemoveOptions(OptionProxyScheme)
				message.RemoveOptions(OptionProxyURI)

				if err := s.sendTo(message, parsedURL.Host); err != nil {
					fmt.Println("send error:", err)
					return
				}

				s.proxyCache.SetDefault(message.GetTokenString()+parsedURL.Host, &proxyNote{addr: senderAddr.String(), tr: s.sr})
				MetricProxySessions.Set(int64(s.proxyCache.ItemCount()))
				MetricProxySessionsRate.Inc()
				<-semaphore
			}()

			continue
		}

		go func() {
			s.processLocalState(message, s.sr)
			<-semaphore
		}()
	}
}

// GetConnectionType возвращает текущий тип соединения
func (s *Server) GetConnectionType() uint8 {
	return s.connectionType
}

// IsTCP возвращает true если сервер использует TCP
func (s *Server) IsTCP() bool {
	return s.connectionType&ConnectionTypeTCP != 0
}

// IsUDP возвращает true если сервер использует UDP
func (s *Server) IsUDP() bool {
	return s.connectionType&ConnectionTypeUDP != 0
}
