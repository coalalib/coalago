package coalago

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/coalalib/coalago/session"
)

func securityOutputLayer(tr *transport, message *CoAPMessage, addr string) error {
	if message.GetScheme() != COAPS_SCHEME {
		return nil
	}

	currentAddr := tr.localAddr()
	setProxyIDIfNeed(message, currentAddr)

	proxyAddr := message.ProxyAddr
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, currentAddr)
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	currentSession, ok := getSessionForAddress(tr, currentAddr, addr, proxyAddr)
	if !ok {
		return ErrorClientSessionNotFound
	}

	if err := encrypt(message, addr, currentSession.AEAD); err != nil {
		return err
	}
	return nil
}

func setProxyIDIfNeed(message *CoAPMessage, senderAddr string) uint32 {
	if message.GetOption(OptionProxyURI) != nil {
		v, ok := proxyIDSessions.Get(message.ProxyAddr + senderAddr)
		if !ok {
			v = rand.Uint32()
			proxyIDSessions.Set(message.ProxyAddr+senderAddr, v)
		}
		message.AddOption(OptionProxySecurityID, v)
		return v.(uint32)
	}
	return 0
}

func getProxyIDIfNeed(proxyAddr string, senderAddr string) (uint32, bool) {
	v, ok := proxyIDSessions.Get(proxyAddr + senderAddr)
	if ok {
		return v.(uint32), ok
	}
	return 0, ok
}

// getSessionForAddress возвращает сессию и продлевает её срок.
func getSessionForAddress(tr *transport, senderAddr, receiverAddr, proxyAddr string) (session.SecuredSession, bool) {
	return tr.sessionStorage().getRefresh(senderAddr, receiverAddr, proxyAddr)
}

func setSessionForAddress(tr *transport, securedSession session.SecuredSession, senderAddr, receiverAddr, proxyAddr string) {
	tr.sessionStorage().Set(senderAddr, receiverAddr, proxyAddr, securedSession)
	MetricSessionsRate.Inc()
}

func deleteSessionForAddress(tr *transport, senderAddr, receiverAddr, proxyAddr string) {
	tr.sessionStorage().Delete(senderAddr, receiverAddr, proxyAddr)
}

func securityInputLayer(tr *transport, message *CoAPMessage, proxyAddr string) error {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, tr.localAddr())
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	if ok, err := receiveHandshake(tr, tr.privateKey, message, proxyAddr); !ok {
		return err
	}

	return handleCoapsScheme(tr, message, proxyAddr)
}

func handleCoapsScheme(tr *transport, message *CoAPMessage, proxyAddr string) error {
	// Check if the message has coaps:// scheme and requires a new Session
	if message.GetScheme() == COAPS_SCHEME {

		addressSession := message.Sender.String()
		currentSession, ok := getSessionForAddress(tr, tr.localAddr(), addressSession, proxyAddr)

		if !ok {
			responseMessage := newMessageWithID(ACK, CoapCodeUnauthorized, message.MessageID, message.Token)
			responseMessage.AddOption(OptionSessionNotFound, 1)
			if _, err := tr.SendTo(responseMessage, message.Sender); err != nil {
				fmt.Println("sendTo error:", err.Error())
			}

			return ErrorClientSessionNotFound
		}

		// Decrypt message payload
		err := decrypt(message, currentSession.AEAD)
		if err != nil {
			deleteSessionForAddress(tr, tr.localAddr(), addressSession, proxyAddr)
			responseMessage := newMessageWithID(ACK, CoapCodeUnauthorized, message.MessageID, message.Token)
			responseMessage.AddOption(OptionSessionExpired, 1)
			if _, err := tr.SendTo(responseMessage, message.Sender); err != nil {
				fmt.Println("sendTo error:", err.Error())
			}

			return ErrorClientSessionExpired
		}

		message.PeerPublicKey = currentSession.PeerPublicKey
	}

	/* Receive Errors */
	sessionNotFound := message.GetOption(OptionSessionNotFound)
	sessionExpired := message.GetOption(OptionSessionExpired)
	if message.Code == CoapCodeUnauthorized {
		if sessionNotFound != nil {
			deleteSessionForAddress(tr, tr.localAddr(), message.Sender.String(), proxyAddr)
			return ErrorSessionNotFound
		}
		if sessionExpired != nil {
			deleteSessionForAddress(tr, tr.localAddr(), message.Sender.String(), proxyAddr)
			return ErrorSessionExpired
		}
	}

	return nil
}

func receiveHandshake(tr *transport, privatekey []byte, message *CoAPMessage, proxyAddr string) (isContinue bool, err error) {
	if message.IsProxies {
		return true, nil
	}
	option := message.GetOption(OptionHandshakeType)
	if option == nil {
		return true, nil
	}

	value := option.IntValue()
	if value != CoapHandshakeTypeClientSignature && value != CoapHandshakeTypeClientHello {
		return false, nil
	}

	peerSession, ok := getSessionForAddress(tr, tr.localAddr(), message.Sender.String(), proxyAddr)
	if !ok {
		if peerSession, err = session.NewSecuredSession(tr.privateKey); err != nil {
			return false, ErrorHandshake
		}
	}
	if value == CoapHandshakeTypeClientHello && message.Payload != nil {
		peerSession.PeerPublicKey = message.Payload.Bytes()

		if err := incomingHandshake(tr, peerSession.Curve.GetPublicKey(), message); err != nil {
			return false, ErrorHandshake
		}
		if signature, err := peerSession.GetSignature(); err == nil {
			if err = peerSession.PeerVerify(signature); err != nil {
				return false, ErrorHandshake
			}
		} else {
			return false, ErrorHandshake
		}

		MetricSuccessfulHandhshakes.Inc()

		peerSession.UpdatedAt = int(time.Now().Unix())
		setSessionForAddress(tr, peerSession, tr.localAddr(), message.Sender.String(), proxyAddr)
		return false, nil
	}

	return false, ErrorHandshake
}

func handshake(tr *transport, message *CoAPMessage, address net.Addr, proxyAddr string) (session.SecuredSession, error) {
	ses, ok := getSessionForAddress(tr, tr.localAddr(), address.String(), proxyAddr)
	if ok {
		return ses, nil

	}

	ses, err := session.NewSecuredSession(tr.privateKey)
	if err != nil {
		return session.SecuredSession{}, err
	}

	// Sending my Public Key.
	// Receiving Peer's Public Key as a Response!
	peerPublicKey, err := sendHelloFromClient(tr, message, ses.Curve.GetPublicKey(), address)
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

	tr.sessionStorage().Set(tr.localAddr(), address.String(), proxyAddr, ses)
	MetricSuccessfulHandhshakes.Inc()

	return ses, nil
}

func sendHelloFromClient(tr *transport, origMessage *CoAPMessage, myPublicKey []byte, address net.Addr) ([]byte, error) {
	var peerPublicKey []byte
	message := newClientHelloMessage(origMessage, myPublicKey)

	respMsg, err := tr.Send(message)
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
	message := newMessage(CON, GET, generateToken(6))
	message.AddOption(OptionHandshakeType, CoapHandshakeTypeClientHello)
	message.Payload = NewBytesPayload(myPublicKey)
	message.CloneOptions(origMessage, OptionProxyURI, OptionProxySecurityID)
	message.ProxyAddr = origMessage.ProxyAddr
	return message
}

func newServerHelloMessage(origMessage *CoAPMessage, publicKey []byte) *CoAPMessage {
	message := newMessageWithID(ACK, CoapCodeContent, origMessage.MessageID, origMessage.Token)
	message.AddOption(OptionHandshakeType, CoapHandshakeTypePeerHello)
	message.Payload = NewBytesPayload(publicKey)
	message.CloneOptions(origMessage, OptionProxySecurityID)
	message.ProxyAddr = origMessage.ProxyAddr
	return message
}

func incomingHandshake(tr *transport, publicKey []byte, origMessage *CoAPMessage) error {
	message := newServerHelloMessage(origMessage, publicKey)
	if _, err := tr.SendTo(message, origMessage.Sender); err != nil {
		return err
	}

	return nil
}
