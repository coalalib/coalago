package coalago

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var StorageLocalStates sync.Map // Используем sync.Map с LoadOrStore

type LocalStateFn func(*CoAPMessage)

type Resourcer interface {
	getResourceForPathAndMethod(path string, method CoapMethod) *CoAPResource
}

type localState struct {
	mx              sync.Mutex
	bufBlock1       map[int][]byte
	totalBlocks     int
	runnedHandler   int32
	downloadStarted time.Time
	r               Resourcer
	tr              *transport
	closeCallback   func()
}

func newLocalState(r Resourcer, tr *transport) *localState {
	return &localState{
		bufBlock1:       make(map[int][]byte),
		totalBlocks:     -1,
		downloadStarted: time.Now(),
		r:               r,
		tr:              tr,
	}
}

func (ls *localState) processMessage(message *CoAPMessage) {
	ls.mx.Lock()
	defer ls.mx.Unlock()

	// Проверка безопасности
	if err := localStateSecurityInputLayer(ls.tr, message, ""); err != nil {
		return
	}

	MetricReceivedMessages.Inc()

	// Локальный обработчик, запускаемый вне критической секции
	localRespHandler := func(msg *CoAPMessage, err error) {
		if atomic.LoadInt32(&ls.runnedHandler) == 1 {
			return
		}
		atomic.StoreInt32(&ls.runnedHandler, 1)
		if err != nil {
			return
		}

		if bq.Has(msg) {
			bq.Write(msg)
			return
		}

		StorageLocalStates.Delete(msg.Sender.String() + msg.GetTokenString())

		requestOnReceive(ls.r.getResourceForPathAndMethod(msg.GetURIPath(), msg.GetMethod()), ls.tr, msg)
	}
	// Обновляем состояние (фрагментация/сборка блоков)
	ls.totalBlocks, ls.bufBlock1 = localStateMessageHandlerSelector(ls.tr, ls.totalBlocks, ls.bufBlock1, message, localRespHandler)
}

func MakeLocalStateFn(r Resourcer, tr *transport, _ func(*CoAPMessage, error)) LocalStateFn {
	ls := newLocalState(r, tr)
	return ls.processMessage
}

func localStateSecurityInputLayer(tr *transport, message *CoAPMessage, proxyAddr string) error {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, tr.conn.LocalAddr().String())
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	if ok, err := receiveHandshake(tr, tr.privateKey, message, proxyAddr); !ok {
		return err
	}

	return handleCoapsScheme(tr, message, proxyAddr)
}

func localStateMessageHandlerSelector(
	sr *transport,
	totalBlocks int,
	buffer map[int][]byte,
	message *CoAPMessage,
	respHandler func(*CoAPMessage, error),
) (int, map[int][]byte) {
	block1 := message.GetBlock1()
	block2 := message.GetBlock2()

	if block1 != nil {
		if message.Type == CON {
			var (
				ok  bool
				err error
			)
			ok, totalBlocks, buffer, message, err = localStateReceiveARQBlock1(sr, totalBlocks, buffer, message)

			if err != nil {
				fmt.Println("localStateMessageHandlerSelector error", err.Error())
			}

			if ok {
				go respHandler(message, err)
			}
		}
		return totalBlocks, buffer
	}

	if block2 != nil {
		if message.Type == ACK {
			id := message.Sender.String() + string(message.Token)
			if c, ok := sr.block2channels.Load(id); ok {
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
