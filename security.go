package coalago

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/coalalib/coalago/session"
)

var (
	SESSIONS_POOL_EXPIRATION = time.Minute * 10
)

func securityOutputLayer(storageSessions sessionStorage, tr *transport, message *CoAPMessage, addr net.Addr) error {
	if message.GetScheme() != COAPS_SCHEME {
		return nil
	}

	setProxyIDIfNeed(message)

	proxyAddr := message.ProxyAddr
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr)
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	currentSession, ok := getSessionForAddress(storageSessions, tr, tr.conn.LocalAddr().String(), addr.String(), proxyAddr)
	if !ok {
		return ErrorSessionNotFound
	}

	if err := encrypt(message, addr, currentSession.AEAD); err != nil {
		return err
	}
	return nil
}

func setProxyIDIfNeed(message *CoAPMessage) uint32 {
	if message.GetOption(OptionProxyURI) != nil {
		v, ok := proxyIDSessions.Load(message.ProxyAddr)
		if !ok {
			v = rand.Uint32()
			proxyIDSessions.Store(message.ProxyAddr, v)
		}
		message.AddOption(OptionProxySecurityID, v)
		return v.(uint32)
	}
	return 0
}

func getProxyIDIfNeed(proxyAddr string) (uint32, bool) {
	v, ok := proxyIDSessions.Load(proxyAddr)
	if ok {
		return v.(uint32), ok
	}
	return 0, ok
}

func getSessionForAddress(storageSessions sessionStorage, tr *transport, senderAddr, receiverAddr, proxyAddr string) (session.SecuredSession, bool) {
	securedSession, ok := storageSessions.Get(senderAddr, receiverAddr, proxyAddr)
	if ok {
		storageSessions.Set(senderAddr, receiverAddr, proxyAddr, securedSession)
	}
	return securedSession, ok
}

func setSessionForAddress(storageSessions sessionStorage, privatekey []byte, securedSession session.SecuredSession, senderAddr, receiverAddr, proxyAddr string) {
	storageSessions.Set(senderAddr, receiverAddr, proxyAddr, securedSession)
	MetricSessionsRate.Inc()
	MetricSessionsCount.Set(int64(storageSessions.ItemCount()))
}

func deleteSessionForAddress(storageSessions sessionStorage, senderAddr, receiverAddr, proxyAddr string) {
	storageSessions.Delete(senderAddr, receiverAddr, proxyAddr)
}

var (
	ErrorSessionNotFound error = errors.New("session not found")
	ErrorSessionExpired  error = errors.New("session expired")
	ErrorHandshake       error = errors.New("error handshake")
)

func securityInputLayer(storageSessions sessionStorage, tr *transport, message *CoAPMessage, proxyAddr string) (isContinue bool, err error) {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr)
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	if ok, err := receiveHandshake(storageSessions, tr, tr.privateKey, message, proxyAddr); !ok {
		return false, err
	}

	// Check if the message has coaps:// scheme and requires a new Session
	if message.GetScheme() == COAPS_SCHEME {
		var addressSession string

		addressSession = message.Sender.String()

		currentSession, ok := getSessionForAddress(storageSessions, tr, tr.conn.LocalAddr().String(), addressSession, proxyAddr)

		if !ok {
			responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
			responseMessage.AddOption(OptionSessionNotFound, 1)
			responseMessage.Token = message.Token
			tr.SendTo(storageSessions, responseMessage, message.Sender)
			return false, ErrorSessionNotFound
		}

		// Decrypt message payload
		err := decrypt(message, currentSession.AEAD)
		if err != nil {
			responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
			responseMessage.AddOption(OptionSessionExpired, 1)
			responseMessage.Token = message.Token
			tr.SendTo(storageSessions, responseMessage, message.Sender)
			return false, ErrorSessionExpired
		}

		message.PeerPublicKey = currentSession.PeerPublicKey
	}

	/* Receive Errors */
	sessionNotFound := message.GetOption(OptionSessionNotFound)
	sessionExpired := message.GetOption(OptionSessionExpired)
	if message.Code == CoapCodeUnauthorized {
		if sessionNotFound != nil {
			deleteSessionForAddress(storageSessions, tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
			return false, ErrorSessionNotFound
		}
		if sessionExpired != nil {
			deleteSessionForAddress(storageSessions, tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
			return false, ErrorSessionExpired
		}
	}

	return true, nil
}

func receiveHandshake(storageSessions sessionStorage, tr *transport, privatekey []byte, message *CoAPMessage, proxyAddr string) (isContinue bool, err error) {
	if message.IsProxies {
		return true, nil
	}
	option := message.GetOption(OptionHandshakeType)
	if option == nil {
		return true, nil
	}

	value := option.IntValue()
	if value != CoapHandshakeTypeClientSignature && value != CoapHandshakeTypeClientHello {
		return true, nil
	}

	peerSession, ok := getSessionForAddress(storageSessions, tr, tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
	if !ok {
		peerSession, err = session.NewSecuredSession(tr.privateKey)
	}
	if value == CoapHandshakeTypeClientHello && message.Payload != nil {
		peerSession.PeerPublicKey = message.Payload.Bytes()

		err := incomingHandshake(storageSessions, tr, peerSession.Curve.GetPublicKey(), message)
		if err != nil {
			return false, ErrorHandshake
		}
		if signature, err := peerSession.GetSignature(); err == nil {
			if err = peerSession.PeerVerify(signature); err != nil {
				return false, ErrorHandshake
			}
		}
		MetricSuccessfulHandhshakes.Inc()

		peerSession.UpdatedAt = int(time.Now().Unix())
		setSessionForAddress(storageSessions, privatekey, peerSession, tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
		return false, nil
	}

	return false, ErrorHandshake
}

const (
	ERR_KEYS_NOT_MATCH = "Expected and current public keys do not match"
)

func handshake(storageSessions sessionStorage, tr *transport, message *CoAPMessage, address net.Addr, proxyAddr string) (session.SecuredSession, error) {
	ses, ok := getSessionForAddress(storageSessions, tr, tr.conn.LocalAddr().String(), address.String(), proxyAddr)
	var err error
	if !ok {
		ses, err = session.NewSecuredSession(tr.privateKey)
		if err != nil {
			return session.SecuredSession{}, err
		}
	}

	// Sending my Public Key.
	// Receiving Peer's Public Key as a Response!
	peerPublicKey, err := sendHelloFromClient(storageSessions, tr, message, ses.Curve.GetPublicKey(), address)
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

	storageSessions.Set(tr.conn.LocalAddr().String(), address.String(), proxyAddr, ses)
	MetricSuccessfulHandhshakes.Inc()

	return ses, nil
}

func sendHelloFromClient(storageSessions sessionStorage, tr *transport, origMessage *CoAPMessage, myPublicKey []byte, address net.Addr) ([]byte, error) {
	var peerPublicKey []byte
	message := newClientHelloMessage(origMessage, myPublicKey)

	respMsg, err := tr.Send(storageSessions, message)
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

func newClientHelloMessage(origMessage *CoAPMessage, myPublicKey []byte) *CoAPMessage {
	message := NewCoAPMessage(CON, POST)
	message.AddOption(OptionHandshakeType, CoapHandshakeTypeClientHello)
	message.Payload = NewBytesPayload(myPublicKey)
	message.Token = generateToken(6)
	message.CloneOptions(origMessage, OptionProxyURI, OptionProxySecurityID)
	message.ProxyAddr = origMessage.ProxyAddr
	return message
}

func newServerHelloMessage(origMessage *CoAPMessage, publicKey []byte) *CoAPMessage {
	message := NewCoAPMessageId(ACK, CoapCodeContent, origMessage.MessageID)
	message.AddOption(OptionHandshakeType, CoapHandshakeTypePeerHello)
	message.Payload = NewBytesPayload(publicKey)
	message.Token = origMessage.Token
	message.CloneOptions(origMessage, OptionProxySecurityID)
	message.ProxyAddr = origMessage.ProxyAddr
	return message
}

func incomingHandshake(storageSessions sessionStorage, tr *transport, publicKey []byte, origMessage *CoAPMessage) error {
	message := newServerHelloMessage(origMessage, publicKey)
	if _, err := tr.SendTo(storageSessions, message, origMessage.Sender); err != nil {
		return err
	}

	return nil
}
