package coalago

import (
	"fmt"
	log "github.com/ndmsystems/golog"
	"github.com/patrickmn/go-cache"
	"sync"
	"sync/atomic"
	"time"
)

var StorageLocalStates = cache.New(sumTimeAttempts, time.Second)

type LocalStateFn func(*CoAPMessage)

func MakeLocalStateFn(r Resourcer, tr *transport, respHandler func(*CoAPMessage, error), closeCallback func()) LocalStateFn {
	var mx sync.Mutex
	var bufBlock1 = make(map[int][]byte)
	var totalBlocks1 = -1
	var runnedHandler int32 = 0
	var downloadStartTime = time.Now()

	return func(message *CoAPMessage) {
		mx.Lock()
		defer mx.Unlock()

		if next, err := localStateSecurityInputLayer(tr, message, ""); err != nil || !next {
			return
		}

		MetricReceivedMessages.Inc()

		respHandler = func(message *CoAPMessage, err error) {
			if atomic.LoadInt32(&runnedHandler) == 1 {
				return
			}
			atomic.StoreInt32(&runnedHandler, 1)

			if err != nil {
				return
			}

			requestOnReceive(r.getResourceForPathAndMethod(message.GetURIPath(), message.GetMethod()), tr, message)
			closeCallback()
			if len(bufBlock1) > 0 {
				log.Info(fmt.Sprintf("U/D: %s, %s",
					ByteCountBinary(int64(len(bufBlock1)*MAX_PAYLOAD_SIZE)),
					ByteCountBinaryBits(int64(len(bufBlock1)*MAX_PAYLOAD_SIZE)*time.Second.Milliseconds()/time.Since(downloadStartTime).Milliseconds())))
			}
		}

		totalBlocks1, bufBlock1 = localStateMessageHandlerSelector(tr, totalBlocks1, bufBlock1, message, respHandler)
	}
}

func localStateSecurityInputLayer(tr *transport, message *CoAPMessage, proxyAddr string) (isContinue bool, err error) {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, tr.conn.LocalAddr().String())
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	if ok, err := receiveHandshake(tr, tr.privateKey, message, proxyAddr); !ok {
		return false, err
	}

	// Check if the message has coaps:// scheme and requires a new Session
	if message.GetScheme() == COAPS_SCHEME {
		addressSession := message.Sender.String()

		currentSession, ok := getSessionForAddress(tr, tr.conn.LocalAddr().String(), addressSession, proxyAddr)

		if !ok {
			responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
			responseMessage.AddOption(OptionSessionNotFound, 1)
			responseMessage.Token = message.Token
			tr.SendTo(responseMessage, message.Sender)
			return false, ErrorClientSessionNotFound
		}

		// Decrypt message payload
		err := decrypt(message, currentSession.AEAD)
		if err != nil {
			deleteSessionForAddress(tr.conn.LocalAddr().String(), addressSession, proxyAddr)
			responseMessage := NewCoAPMessageId(ACK, CoapCodeUnauthorized, message.MessageID)
			responseMessage.AddOption(OptionSessionExpired, 1)
			responseMessage.Token = message.Token
			tr.SendTo(responseMessage, message.Sender)
			return false, ErrorClientSessionExpired
		}

		message.PeerPublicKey = currentSession.PeerPublicKey
	}

	/* Receive Errors */
	sessionNotFound := message.GetOption(OptionSessionNotFound)
	sessionExpired := message.GetOption(OptionSessionExpired)
	if message.Code == CoapCodeUnauthorized {
		if sessionNotFound != nil {
			deleteSessionForAddress(tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
			return false, ErrorSessionNotFound
		}
		if sessionExpired != nil {
			deleteSessionForAddress(tr.conn.LocalAddr().String(), message.Sender.String(), proxyAddr)
			return false, ErrorSessionExpired
		}
	}

	return true, nil
}

func localStateMessageHandlerSelector(
	sr *transport,
	totalBlocks int,
	buffer map[int][]byte,

	message *CoAPMessage,
	respHandler func(*CoAPMessage, error),
) (
	int, map[int][]byte,
) {
	block1 := message.GetBlock1()
	block2 := message.GetBlock2()

	if block1 != nil {
		if message.Type == CON {
			var (
				ok  bool
				err error
			)
			ok, totalBlocks, buffer, message, err = localStateReceiveARQBlock1(sr, totalBlocks, buffer, message)
			if ok {
				go respHandler(message, err)
			}
		}
		return totalBlocks, buffer
	}

	if block2 != nil {
		if message.Type == ACK {
			id := message.Sender.String() + string(message.Token)

			c, ok := sr.block2channels.Load(id)
			if ok {
				c.(chan *CoAPMessage) <- message
			}
		}
		return totalBlocks, buffer
	}
	go respHandler(message, nil)
	return totalBlocks, buffer
}

func localStateReceiveARQBlock1(sr *transport, totalBlocks int, buf map[int][]byte, inputMessage *CoAPMessage) (bool, int, map[int][]byte, *CoAPMessage, error) {
	block := inputMessage.GetBlock1()
	if block == nil || inputMessage.Type != CON {
		return false, totalBlocks, buf, inputMessage, nil
	}
	if !block.MoreBlocks {
		totalBlocks = block.BlockNumber + 1
	}

	buf[block.BlockNumber] = inputMessage.Payload.Bytes()
	if totalBlocks == len(buf) {
		b := []byte{}
		for i := 0; i < totalBlocks; i++ {
			b = append(b, buf[i]...)
		}
		inputMessage.Payload = NewBytesPayload(b)
		return true, totalBlocks, buf, inputMessage, nil
	}

	var ack *CoAPMessage
	w := inputMessage.GetOption(OptionSelectiveRepeatWindowSize)
	if w != nil {
		ack = ackToWithWindowOffset(nil, inputMessage, CoapCodeContinue, w.IntValue(), block.BlockNumber, buf)
	} else {
		ack = ackTo(nil, inputMessage, CoapCodeContinue)
	}

	if err := sr.sendToSocketByAddress(ack, inputMessage.Sender); err != nil {
		return false, totalBlocks, buf, inputMessage, err
	}

	return false, totalBlocks, buf, inputMessage, nil
}
