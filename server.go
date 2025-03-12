package coalago

import (
	"fmt"
	"net"
	"strings"
	"sync"

	log "github.com/ndmsystems/golog"
)

type rawData struct {
	buff   []byte
	sender net.Addr
}

type Server struct {
	proxyEnable bool
	sr          *transport
	resources   sync.Map
	privatekey  []byte
	bq          backwardStorage
  addr        string // сохраняем адрес для Refresh()

}

func NewServer() *Server {
	return &Server{
		bq: backwardStorage{
			m: make(map[uint16]chan *CoAPMessage),
		},
	}
}

func NewServerWithPrivateKey(privatekey []byte) *Server {
	return &Server{privatekey: privatekey}
}

type Resourcer interface {
	getResourceForPathAndMethod(path string, method CoapMethod) *CoAPResource
}

func (s *Server) Listen(addr string) error {
	s.addr = addr // сохраняем адрес для будущего рестарта
	conn, err := newListener(addr)
	if err != nil {
		return err
	}

	s.sr = newtransport(conn)
	s.sr.privateKey = s.privatekey
	log.Info(fmt.Sprintf(
		"COALAServer start ADDR: %s, WS: %d, MinWS: %d, MaxWS: %d, Retransmit:%d, timeWait:%d, poolExpiration:%d",
		addr, DEFAULT_WINDOW_SIZE, MIN_WiNDOW_SIZE, MAX_WINDOW_SIZE, maxSendAttempts, timeWait, SESSIONS_POOL_EXPIRATION))

	s.listenLoop() // блокирующий цикл прослушивания
	return nil
}

func (s *Server) listenLoop() {
	for {
		readBuf := make([]byte, MTU+1)
		n, senderAddr, err := s.sr.conn.Listen(readBuf)
		if err != nil {
			panic(err)
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

		if s.bq.Has(message.MessageID) {
			s.bq.Write(message)
			goto start
		}

		id := senderAddr.String() + message.GetTokenString()
		fn, _ := StorageLocalStates.LoadOrStore(id, MakeLocalStateFn(s, s.sr, nil, func() {
			StorageLocalStates.Delete(id)
		}))
		go fn.(LocalStateFn)(message)
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
	conn, err := newListener(s.addr)
	if err != nil {
		return err
	}
	s.sr = newtransport(conn)
	s.sr.privateKey = s.privatekey

	go s.listenLoop() // перезапускаем цикл прослушивания в горутине
	log.Info(fmt.Sprintf("COALAServer refreshed on ADDR: %s", s.addr))
	return nil
}

func (s *Server) Serve(conn *net.UDPConn) {
	c := &connection{conn: conn}
	s.sr = newtransport(c)
	s.sr.privateKey = s.privatekey
}

func (s *Server) ServeMessage(message *CoAPMessage) {
	id := message.Sender.String() + message.GetTokenString()
	fn, _ := StorageLocalStates.LoadOrStore(id, MakeLocalStateFn(s, s.sr, nil, func() {
		StorageLocalStates.Delete(id)
	}))
	go fn.(LocalStateFn)(message)
}

func (s *Server) addResource(res *CoAPResource) {
	key := res.Path + fmt.Sprint(res.Method)
	s.resources.Store(key, res)
}

func (s *Server) GET(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodGet, path, handler))
}

func (s *Server) POST(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodPost, path, handler))
}

func (s *Server) AddPUTResource(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodPut, path, handler))
}

func (s *Server) DELETE(path string, handler CoAPResourceHandler) {
	s.addResource(NewCoAPResource(CoapMethodDelete, path, handler))
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

func (s *Server) EnableProxy() {
	s.proxyEnable = true
}

func (s *Server) DisableProxy() {
	s.proxyEnable = false
}

func (s *Server) SetPrivateKey(privateKey []byte) {
	s.privatekey = privateKey
}

func (s *Server) GetPrivateKey() []byte {
	return s.privatekey
}

func (s *Server) SendToSocket(message *CoAPMessage, addr string) error {
	b, err := Serialize(message)
	if err != nil {
		return err
	}
	_, err = s.sr.conn.WriteTo(b, addr)
	return err
}

func (s *Server) Send(message *CoAPMessage, addr string) (*CoAPMessage, error) {
	err := s.SendToSocket(message, addr)
	if err != nil {
		return nil, err
	}

	return s.bq.Read(message.MessageID)
}
