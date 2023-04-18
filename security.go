package newcoala

import (
	"errors"
	"net"
	"time"

	"github.com/ndmsystems/gum-server/services/DataService/newcoala/session"
	"github.com/patrickmn/go-cache"
)

var (
	ErrorSessionNotFound       error = errors.New("session not found")
	ErrorClientSessionNotFound error = errors.New("client session not found")
	ErrorSessionExpired        error = errors.New("session expired")
	ErrorClientSessionExpired  error = errors.New("client session expired")
	ErrorHandshake             error = errors.New("error handshake")
)

const (
	sessionLifetime = time.Minute*4 + time.Second*9
)

type sessionState struct {
	key string
	est time.Time
}

type securitySessionStorage struct {
	// rwmx sync.RWMutex
	// m       map[string]session.SecuredSession
	// indexes map[string]time.Time
	// est     []sessionState
	seccache *cache.Cache
}

func newSecuritySessionStorage() *securitySessionStorage {
	s := &securitySessionStorage{
		// m:       make(map[string]session.SecuredSession),
		// indexes: make(map[string]time.Time),
		seccache: cache.New(sessionLifetime, time.Second),
	}

	// go func() {
	// 	for {
	// 		s.cleanup()
	// 		time.Sleep(time.Second)
	// 	}
	// }()

	return s
}

// func (s *securitySessionStorage) cleanup() {
// 	now := time.Now()
// 	l := len(s.est)
// 	for i := 0; i < l; i++ {
// 		s.rwmx.RLock()
// 		state := s.est[i]
// 		s.rwmx.RUnlock()

// 		if state.est.Sub(now) > 0 {
// 			return
// 		}

// 		s.rwmx.Lock()
// 		s.est = append(s.est[:i], s.est[i+1:]...)
// 		i--

// 		t := s.indexes[state.key]

// 		if time.Since(t) >= sessionLifetime {
// 			delete(s.m, state.key)
// 			delete(s.indexes, state.key)
// 		} else {
// 			est := now.Add(sessionLifetime)
// 			s.indexes[state.key] = now

// 			s.est = append(s.est, sessionState{
// 				est: est,
// 				key: state.key,
// 			})
// 		}
// 		s.rwmx.Unlock()

// 	}
// }

func (s *securitySessionStorage) Set(k string, v session.SecuredSession) {
	// now := time.Now()
	// est := now.Add(sessionLifetime)
	// s.rwmx.Lock()
	// s.m[k] = v
	// s.indexes[k] = now
	// s.est = append(s.est, sessionState{
	// 	est: est,
	// 	key: k,
	// })

	// s.rwmx.Unlock()
	s.seccache.SetDefault(k, v)
}

func (s *securitySessionStorage) Delete(k string) {
	// s.rwmx.Lock()
	// delete(s.m, k)
	// delete(s.indexes, k)
	// s.rwmx.Unlock()
	s.seccache.Delete(k)
}

func (s *securitySessionStorage) Update(k string, sess session.SecuredSession) {
	// s.rwmx.Lock()
	// s.indexes[k] = time.Now()
	// s.m[k] = sess
	// s.rwmx.Unlock()
	s.seccache.SetDefault(k, sess)
}

func (s *securitySessionStorage) Get(k string) (sess session.SecuredSession, ok bool) {
	// s.rwmx.RLock()
	// sess, ok := s.m[k]
	// s.rwmx.RUnlock()

	v, ok := s.seccache.Get(k)
	if !ok {
		return sess, ok
	}

	sess = v.(session.SecuredSession)
	return sess, ok
}

func (s *Server) securityOutputLayer(pc net.PacketConn, message *CoAPMessage, addr net.Addr) error {
	if message.GetScheme() != COAPS_SCHEME {
		return nil
	}
	session, ok := s.secSessions.Get(addr.String())
	if !ok {
		return ErrorClientSessionNotFound
	}

	if err := encrypt(message, addr, session.AEAD); err != nil {
		return err
	}

	return nil
}

func (s *Server) securityInputLayer(pc net.PacketConn, privateKey []byte, message *CoAPMessage) (isContinue bool, err error) {
	option := message.GetOption(OptionHandshakeType)
	if option != nil {
		go s.receiveHandshake(pc, privateKey, option, message)
		return false, nil
	}

	// Check if the message has coaps:// scheme and requires a new Session
	if message.GetScheme() == COAPS_SCHEME {
		currentSession, ok := s.secSessions.Get(message.Sender.String())
		if !ok {
			go func() {
				responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
				responseMessage.AddOption(OptionSessionNotFound, 1)
				responseMessage.Token = message.Token
				if b, err := Serialize(responseMessage); err == nil {
					MetricSentMessages.Inc()
					pc.WriteTo(b, message.Sender)
				}
			}()
			return false, ErrorClientSessionNotFound
		}

		// Decrypt message payload
		err := decrypt(message, currentSession.AEAD)
		if err != nil {
			s.secSessions.Delete(message.Sender.String())

			responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
			responseMessage.AddOption(OptionSessionExpired, 1)
			responseMessage.Token = message.Token

			if b, err := Serialize(responseMessage); err == nil {
				MetricSentMessages.Inc()
				pc.WriteTo(b, message.Sender)
			}

			return false, ErrorClientSessionExpired
		}

		s.secSessions.Update(message.Sender.String(), currentSession)

		// s.secSessions.Set(message.Sender.String(), currentSession)
		message.PeerPublicKey = currentSession.PeerPublicKey
	}

	/* Receive Errors */
	sessionNotFound := message.GetOption(OptionSessionNotFound)
	sessionExpired := message.GetOption(OptionSessionExpired)
	if message.Code == CoapCodeUnauthorized {
		if sessionNotFound != nil {
			s.secSessions.Delete(message.Sender.String())
			return false, ErrorSessionNotFound
		}
		if sessionExpired != nil {
			s.secSessions.Delete(message.Sender.String())
			return false, ErrorSessionExpired
		}
	}

	return true, nil
}

func (s *Server) receiveHandshake(pc net.PacketConn, privatekey []byte, option *CoAPMessageOption, message *CoAPMessage) (isContinue bool, err error) {
	value := option.IntValue()
	if value != CoapHandshakeTypeClientSignature && value != CoapHandshakeTypeClientHello {
		return false, nil
	}
	peerSession, ok := s.secSessions.Get(message.Sender.String())

	if !ok {
		if peerSession, err = session.NewSecuredSession(privatekey); err != nil {
			return false, ErrorHandshake
		}
	}
	if value == CoapHandshakeTypeClientHello && message.Payload != nil {
		peerSession.PeerPublicKey = message.Payload.Bytes()

		if err := incomingHandshake(pc, peerSession.Curve.GetPublicKey(), message); err != nil {
			return false, ErrorHandshake
		}

		if signature, err := peerSession.GetSignature(); err == nil {
			if err = peerSession.PeerVerify(signature); err != nil {
				return false, ErrorHandshake
			}
		}

		s.secSessions.Set(message.Sender.String(), peerSession)
		MetricSessionsRate.Inc()
		// MetricSessionsCount.Set(int64(len(s.secSessions.m)))
		MetricSessionsCount.Set(int64(s.secSessions.seccache.ItemCount()))

		MetricSuccessfulHandhshakes.Inc()
		return false, nil
	}

	return false, ErrorHandshake
}

const (
	ERR_KEYS_NOT_MATCH = "Expected and current public keys do not match"
)

func newServerHelloMessage(origMessage *CoAPMessage, publicKey []byte) *CoAPMessage {
	message := NewCoAPMessageId(ACK, CoapCodeContent, origMessage.MessageID)
	message.AddOption(OptionHandshakeType, CoapHandshakeTypePeerHello)
	message.Payload = NewBytesPayload(publicKey)
	message.Token = origMessage.Token
	message.CloneOptions(origMessage, OptionProxySecurityID)
	message.ProxyAddr = origMessage.ProxyAddr
	return message
}

func incomingHandshake(pc net.PacketConn, publicKey []byte, origMessage *CoAPMessage) error {
	message := newServerHelloMessage(origMessage, publicKey)
	b, err := Serialize(message)
	if err != nil {
		return err
	}
	_, err = pc.WriteTo(b, origMessage.Sender)
	return err
}
