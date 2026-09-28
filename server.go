package coalago

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coalalib/coalago/session"
)

const (
	ConnectionTypeUDP = 1 << iota // 1
	ConnectionTypeTCP             // 2
)

// proxyNote — куда вернуть ответ на проксированный запрос: отправитель запроса и сокет,
// с которого он пришёл.
type proxyNote struct {
	addr net.Addr
	tr   *transport
}

type Server struct {
	proxyEnable    bool
	sr             *transport
	resources      atomic.Pointer[resourceMap]
	resourcesMu    sync.Mutex // сериализует регистрацию ресурсов (копирование карты)
	privatekey     []byte
	addr           string      // сохраняем адрес для Refresh()
	connectionType uint8       // битовая маска для TCP/UDP
	proxies        *proxyTable // (адрес назначения, токен) -> proxyNote
	// sessions держит шифрованные сессии ЭТОГО сервера. Хранилище не может быть общим
	// на процесс: ключ сессии не содержит локальный адрес для проксированных пиров, и
	// два сервера в одном бинарнике (со своими ключами) перетирали бы сессии друг друга
	// для одного и того же peer+proxy.
	sessions *sessionStorageImpl

	tcpLn net.Listener // TCP-accept-листенер из listenTCP; нужен только чтобы Close() мог его закрыть
	srMu  sync.Mutex   // защищает s.sr и s.tcpLn от гонки между Close/Refresh/Listen/listenTCP

	closeOnce sync.Once // делает Close идемпотентным
	closeErr  error     // результат первого Close; последующие вызовы возвращают его же
}

func NewServer(opts ...Opt) *Server {
	options := &coalaopts{}
	for _, opt := range opts {
		opt(options)
	}

	return &Server{
		privatekey: options.privatekey,
		proxies:    newProxyTable(time.Minute),
		sessions:   newSessionStorageImpl(SESSIONS_POOL_EXPIRATION),
	}
}

// newServerTransport создает transport, привязанный к хранилищу сессий этого сервера.
func (s *Server) newServerTransport(conn Transport) *transport {
	tr := newtransport(conn)
	tr.sessions = s.sessions
	return tr
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

	s.srMu.Lock()
	s.sr = s.newServerTransport(conn)
	s.sr.privateKey = s.privatekey
	s.srMu.Unlock()
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

	s.srMu.Lock()
	s.tcpLn = ln
	s.srMu.Unlock()

	fmt.Printf("COALA TCP server start ADDR: %s\n", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Close() закрывает ln напрямую: без этой проверки Accept() после
			// закрытия листенера возвращал бы ошибку немедленно (не блокируясь),
			// и цикл крутился бы в busy-spin вместо штатного выхода.
			if strings.Contains(err.Error(), "use of closed network connection") {
				fmt.Println("tcp listener was closed")
				return nil
			}
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

	tcpTr := s.newServerTransport(&tcpConnection{conn: conn.(*net.TCPConn)})

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

		// Разбор копирует данные: buf переиспользуется следующим кадром, пока сообщение
		// ещё обрабатывается.
		msg, err := Deserialize(buf[:n])
		if err != nil {
			fmt.Println("deserialize error:", err)
			continue
		}

		msg.Sender = conn.RemoteAddr()
		if msg.GetOptionProxyURIasString() == "" {
			if note, ok := s.proxies.get(messageKey(msg)); ok {
				note.tr.writeTo(buf[:n], note.addr)
				continue
			}

			option := msg.GetOption(OptionHandshakeType)
			if option != nil && option.IntValue() == CoapHandshakeTypePeerHello && bq.Has(msg) {
				bq.Write(msg)
				continue
			}

			workers.submit(rxTask{s: s, tr: tcpTr, msg: msg})
			continue
		}

		workers.submit(rxTask{s: s, tr: tcpTr, msg: msg, proxy: true})
	}
}

// forwardProxy пересылает сообщение по его Proxy-URI и запоминает обратный маршрут.
// Слот семафора (sem) освобождается на любом выходе: ранний return иначе навсегда
// съедал бы слот, и после maxParallel ошибок listenLoop перестал бы принимать пакеты.
func (s *Server) forwardProxy(message *CoAPMessage, tr *transport, sem chan struct{}) {
	defer releaseSlot(sem)

	parsedURL, err := url.Parse(message.GetOptionProxyURIasString())
	if err != nil {
		fmt.Println("parse proxyUri error:", err)
		return
	}

	message.RemoveOptions(OptionProxyScheme)
	message.RemoveOptions(OptionProxyURI)

	if err := s.sendMultyProxy(message, parsedURL.Host); err != nil {
		fmt.Println("send error:", err)
		return
	}

	s.proxies.set(exKeyOf(peerKeyString(parsedURL.Host), message.Token), &proxyNote{addr: message.Sender, tr: tr})
	MetricProxySessions.Set(int64(s.proxies.itemCount()))
	MetricProxySessionsRate.Inc()
}

func (s *Server) Refresh() error {
	if s.addr == "" {
		return fmt.Errorf("server address not set")
	}

	s.srMu.Lock()
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
		s.srMu.Unlock()
		return err
	}

	s.sr = s.newServerTransport(conn)
	s.sr.privateKey = s.privatekey
	s.srMu.Unlock()

	go s.listenLoop() // перезапускаем цикл прослушивания в горутине
	fmt.Printf("server refreshed on ADDR: %s", s.addr)
	return nil
}

// Close останавливает сервер, поднятый Listen() и/или ListenTCP(): закрывает
// нижележащие сетевые соединения тем же приёмом, что и Refresh(), но не
// открывает их заново. Закрытие s.sr.conn (UDP) и s.tcpLn (TCP accept-листенер)
// разблокирует блокирующие вызовы чтения внутри listenLoop() и accept-цикла
// listenTCP(), которые штатно завершаются сами, увидев ошибку
// "use of closed network connection" — паники не будет.
//
// Идемпотентен: повторные вызовы не делают ничего и возвращают тот же результат,
// что и первый вызов. Потокобезопасен относительно конкурентного Refresh(),
// Listen()/ListenTCP() и уже запущенного listenLoop()/listenTCP().
//
// Замечание: если сервер был поднят через Serve(conn) (встраивание в чужой
// UDP-сокет, используется прокси-сервисом) — s.sr указывает на тот же переданный
// conn, что и при Listen(), поэтому Close() закроет и его, как и Refresh() уже
// делает сегодня. Владельцу внешнего сокета в этом сценарии Close() вызывать не нужно.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.srMu.Lock()
		defer s.srMu.Unlock()

		if s.sr != nil && s.sr.conn != nil {
			if closer, ok := s.sr.conn.(interface{ Close() error }); ok {
				s.closeErr = closer.Close()
			}
		}

		if s.tcpLn != nil {
			if err := s.tcpLn.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
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

func (s *Server) sendMultyProxy(message *CoAPMessage, addr string) error {
	// Do not act as an open relay: only forward Proxy-URI messages when proxy
	// mode has been explicitly enabled via Server.Proxy(true). Without this
	// guard any peer could use the server as an SSRF pivot to arbitrary hosts.
	if !s.proxyEnable {
		return errors.New("proxy disabled")
	}

	tr := s.sr
	if conn, ok := connStorage.GetTCP(addr); ok {
		tr = s.newServerTransport(&tcpConnection{conn: conn.(*net.TCPConn)})
	}

	buf, err := Serialize(message)
	if err != nil {
		return err
	}

	_, err = tr.conn.WriteTo(buf, addr)
	if err != nil {
		return err
	}

	return nil
}

func (s *Server) sendTo(message *CoAPMessage, addr string) error {
	tr := s.sr
	if conn, ok := connStorage.GetTCP(addr); ok {
		tr = s.newServerTransport(&tcpConnection{conn: conn.(*net.TCPConn)})
	}

	secMessage := message.Clone(true)
	if err := securityOutputLayer(tr, secMessage, addr); err != nil {
		return err
	}

	if secMessage.AddChecksumOnSend {
		if err := applyChecksum(secMessage); err != nil {
			return err
		}
	}

	buf, err := Serialize(secMessage)
	if err != nil {
		return err
	}

	_, err = tr.conn.WriteTo(buf, addr)
	if err != nil {
		return err
	}

	return nil
}

type sendOpts struct {
	retries int
}

type SendOptions func(*sendOpts)

func WithRetries(retries int) SendOptions {
	return func(opts *sendOpts) {
		opts.retries = retries
	}
}

func (s *Server) Send(message *CoAPMessage, addr string, opts ...SendOptions) (*CoAPMessage, error) {
	if message.GetScheme() != COAPS_SCHEME {
		return s.send(message, addr, opts...)
	}

	tr := s.sr
	if conn, ok := connStorage.GetTCP(addr); ok {
		tr = s.newServerTransport(&tcpConnection{conn: conn.(*net.TCPConn)})
	}

	proxyAddr := message.ProxyAddr
	if len(proxyAddr) > 0 {
		proxyID := setProxyIDIfNeed(message, tr.localAddr())
		proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
	}

	_, err := s.serverHandshake(tr, message, addr, proxyAddr)
	if err != nil {
		return nil, err
	}

	message.Timeout = time.Second
	msg, err := s.send(message, addr)
	if err == nil {
		return msg, nil
	}

	if !slices.Contains([]error{ErrorSessionExpired, ErrorSessionNotFound, ErrorClientSessionExpired, ErrorClientSessionNotFound}, err) {
		return nil, err
	}

	_, err = s.serverHandshake(tr, message, addr, proxyAddr)
	if err != nil {
		return nil, err
	}

	return s.send(message, addr, opts...)
}

// Send отправляет сообщение на указанный адрес и возвращает ответ
// используется вместо клиента, когда нужно отправить запрос с занятого сервером порта
func (s *Server) send(message *CoAPMessage, addr string, opts ...SendOptions) (*CoAPMessage, error) {
	o := &sendOpts{
		retries: 0,
	}

	for _, opt := range opts {
		opt(o)
	}

	if message.Timeout == 0 {
		message.Timeout = time.Second
	}

	// Ожидание регистрируется до отправки: ответ, опередивший регистрацию, ушёл бы в
	// обработку как обычное сообщение, обмен пометился бы обработанным, и ответы на
	// повторы отбрасывались бы как дубли.
	resolved, _ := net.ResolveUDPAddr("udp", addr)
	id := exKeyOf(peerKeyOf(resolved), message.Token)
	ch := bq.Get(id)

	defer bq.Delete(id)

	if err := s.sendTo(message, addr); err != nil {
		return nil, err
	}

	for range o.retries + 1 {
		select {
		case msg := <-ch:
			return msg, nil
		case <-time.After(message.Timeout):
			if err := s.sendTo(message, addr); err != nil {
				return nil, err
			}
		}
	}

	return nil, errors.New("timeout")
}

func (s *Server) serverHandshake(tr *transport, message *CoAPMessage, address string, proxyAddr string) (session.SecuredSession, error) {
	ses, ok := getSessionForAddress(tr, tr.localAddr(), address, proxyAddr)
	if ok {
		return ses, nil
	}

	ses, err := session.NewSecuredSession(tr.privateKey)
	if err != nil {
		return session.SecuredSession{}, err
	}

	// Sending my Public Key.
	// Receiving Peer's Public Key as a Response!
	peerPublicKey, err := s.sendHelloFromServer(message, ses.Curve.GetPublicKey(), address)
	if err != nil {
		return session.SecuredSession{}, err
	}

	// assign new value
	ses.PeerPublicKey = peerPublicKey

	signature, err := ses.GetSignature()
	if err != nil {
		return session.SecuredSession{}, err
	}

	err = ses.Verify(signature)
	if err != nil {
		return session.SecuredSession{}, err
	}

	tr.sessionStorage().Set(tr.localAddr(), address, proxyAddr, ses)
	MetricSuccessfulHandhshakes.Inc()

	return ses, nil
}

func (s *Server) sendHelloFromServer(origMessage *CoAPMessage, myPublicKey []byte, addr string) ([]byte, error) {
	var peerPublicKey []byte
	message := newClientHelloMessage(origMessage, myPublicKey)

	respMsg, err := s.Send(message, addr)
	if err != nil {
		return nil, err
	}

	if respMsg == nil {
		return nil, nil
	}

	optHandshake := respMsg.GetOption(OptionHandshakeType)
	if optHandshake != nil {
		if optHandshake.IntValue() == CoapHandshakeTypePeerHello {
			peerPublicKey = respMsg.Payload.Bytes()
		}
	}

	if origMessage.BreakConnectionOnPK != nil {
		if origMessage.BreakConnectionOnPK(peerPublicKey) {
			return nil, errors.New(ERR_KEYS_NOT_MATCH)
		}
	}

	return peerPublicKey, err
}

// Serve запускает сервер на указанном соединении (например, если нужно использовать свой UDP-сервер)
// нужно для прокси сервиса
func (s *Server) Serve(conn *net.UDPConn) {
	c := &connection{conn: conn}
	s.sr = s.newServerTransport(c)
	s.sr.privateKey = s.privatekey
}

// ServeMessage обрабатывает сообщение, как если бы оно пришло от клиента
// нужно для прокси сервиса
func (s *Server) ServeMessage(message *CoAPMessage) {
	workers.submit(rxTask{s: s, tr: s.sr, msg: message})
}

// resourceMap — ресурсы по методу и пути. Карта неизменяемая: регистрация копирует её,
// а поиск на каждом сообщении читает без замков. Раньше ключом была строка путь+метод
// через fmt.Sprint, sync.Map и сборка пути сообщения — 4 аллокации на поиск.
type resourceMap map[CoapMethod]map[string]*CoAPResource

func (s *Server) addResource(res *CoAPResource) {
	s.resourcesMu.Lock()
	defer s.resourcesMu.Unlock()

	next := make(resourceMap)
	if cur := s.resources.Load(); cur != nil {
		for method, byPath := range *cur {
			next[method] = make(map[string]*CoAPResource, len(byPath))
			for path, r := range byPath {
				next[method][path] = r
			}
		}
	}
	if next[res.Method] == nil {
		next[res.Method] = make(map[string]*CoAPResource)
	}
	next[res.Method][res.Path] = res
	s.resources.Store(&next)
}

// resourceFor — ресурс для запроса: сначала «*» метода, затем путь сообщения без
// крайних «/» и пробелов. Путь собирается в буфере на стеке, без аллокации.
func (s *Server) resourceFor(msg *CoAPMessage) *CoAPResource {
	resources := s.resources.Load()
	if resources == nil {
		return nil
	}
	byPath := (*resources)[msg.GetMethod()]
	if res, ok := byPath["*"]; ok {
		return res
	}
	var buf [128]byte
	return byPath[string(bytes.Trim(msg.appendURIPath(buf[:0]), "/ "))]
}

func (s *Server) listenLoop() {
	semaphore := make(chan struct{}, maxParallel)
	// Один буфер на цикл: разбор копирует токен, опции и тело, сообщение буфер не держит.
	readBuf := make([]byte, MTU+1)

	for {
		tr := s.sr
		var (
			n          int
			senderAddr net.Addr
			err        error
		)
		if l, ok := tr.conn.(udpAddrListener); ok {
			var from *net.UDPAddr
			if n, from, err = l.listenUDPAddr(readBuf); err == nil {
				senderAddr = from
			}
		} else {
			n, senderAddr, err = tr.conn.Listen(readBuf)
		}
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				fmt.Println("connection was closed")
				return
			}
			fmt.Printf("read error: %v\n", err)
			continue
		}

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

		if note, ok := s.proxies.get(messageKey(message)); ok {
			note.tr.writeTo(readBuf[:n], note.addr)
			continue
		}

		semaphore <- struct{}{}

		if message.GetOptionProxyURIasString() != "" {
			workers.submit(rxTask{s: s, tr: tr, msg: message, sem: semaphore, proxy: true})
			continue
		}

		// обработка ответных хендшейков после send
		option := message.GetOption(OptionHandshakeType)
		if option != nil && option.IntValue() == CoapHandshakeTypePeerHello && bq.Has(message) {
			bq.Write(message)
			<-semaphore
			continue
		}

		workers.submit(rxTask{s: s, tr: tr, msg: message, sem: semaphore})
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
