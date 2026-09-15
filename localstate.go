package coalago

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var StorageLocalStates = newShardedCache(3 * time.Minute)

// ProcessedMessages — лёгкий дедупер уже обработанных запросов.
// Ключ: sender+token, значение: struct{}{}. Запись живёт processedTTL и
// нужна, чтобы отбросить запоздалые ретрансмиты без создания localState.
var ProcessedMessages = newShardedCache(processedTTL)

const processedTTL = 10 * time.Second

type LocalStateFn func(*CoAPMessage)

type Resourcer interface {
	getResourceForPathAndMethod(path string, method CoapMethod) *CoAPResource
}

type localState struct {
	mx          sync.Mutex
	bufBlock1   map[int][]byte
	totalBlocks int
	// block2-ответ на запрос, отправленный через Server.Send (см. localStateReceiveARQBlock2)
	bufBlock2       map[int][]byte
	totalBlocks2    int
	runnedHandler   int32
	downloadStarted time.Time
	r               Resourcer
	tr              *transport
}

func newLocalState(r Resourcer, tr *transport) *localState {
	return &localState{
		bufBlock1:       make(map[int][]byte),
		totalBlocks:     -1,
		bufBlock2:       make(map[int][]byte),
		totalBlocks2:    -1,
		downloadStarted: time.Now(),
		r:               r,
		tr:              tr,
	}
}

func (ls *localState) processMessage(message *CoAPMessage) {
	ls.mx.Lock()
	defer ls.mx.Unlock()

	// Проверка безопасности
	if ok, err := localStateSecurityInputLayer(ls.tr, message, ""); !ok || err != nil {
		// 4.01 о потерянной/протухшей сессии в ответ на НАШ запрос отдаём ожидающему
		// Server.send: иначе он узнаёт о ней только по таймауту, когда следующий sendTo
		// не находит уже удалённую сессию
		if err != nil && isSessionError(err) && bq.Has(message) {
			bq.Write(message)
		}
		return
	}

	MetricReceivedMessages.Inc()

	// Локальный обработчик, запускаемый вне критической секции.
	// Дедупликация ретрансмитов:
	//   - первая прошедшая CAS горутина запускает хэндлер ровно один раз,
	//     остальные (включая пришедшие во время выполнения) выходят сразу;
	//   - после возврата из хэндлера запись из StorageLocalStates удаляется
	//     сразу, а ключ кладётся в ProcessedMessages на processedTTL — там
	//     его и ловят запоздалые ретрансмиты (см. Server.processLocalState).
	localRespHandler := func(msg *CoAPMessage, err error) {
		if !atomic.CompareAndSwapInt32(&ls.runnedHandler, 0, 1) {
			return
		}
		id := msg.Sender.String() + msg.GetTokenString()
		defer func() {
			StorageLocalStates.Delete(id)
			ProcessedMessages.Set(id, struct{}{})
		}()

		if err != nil {
			return
		}

		if bq.Has(msg) {
			bq.Write(msg)
			return
		}

		requestOnReceive(ls.r.getResourceForPathAndMethod(msg.GetURIPath(), msg.GetMethod()), ls.tr, msg)
	}
	// Обновляем состояние (фрагментация/сборка блоков)
	localStateMessageHandlerSelector(ls, message, localRespHandler)
}

func MakeLocalStateFn(r Resourcer, tr *transport, _ func(*CoAPMessage, error)) LocalStateFn {
	ls := newLocalState(r, tr)
	return ls.processMessage
}

func localStateSecurityInputLayer(tr *transport, message *CoAPMessage, proxyAddr string) (bool, error) {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, tr.conn.LocalAddr().String())
		if ok {
			proxyAddr = fmt.Sprintf("%v%v", proxyAddr, proxyID)
		}
	}

	if ok, err := receiveHandshake(tr, tr.privateKey, message, proxyAddr); !ok {
		return false, err
	}

	if err := handleCoapsScheme(tr, message, proxyAddr); err != nil {
		return false, err
	}

	return true, nil
}

func localStateMessageHandlerSelector(
	ls *localState,
	message *CoAPMessage,
	respHandler func(*CoAPMessage, error),
) {
	sr := ls.tr
	block1 := message.GetBlock1()
	block2 := message.GetBlock2()

	// Преамбула block2-ответа на наш запрос (Server.send): пустой ACK с размером окна,
	// за которым пойдут блоки. В respHandler её отдавать нельзя: он одноразовый, и после
	// него все блоки отбрасывались бы как ретрансмиты уже обработанного токена, а Send
	// получал бы пустой ответ вместо тела.
	if isBlock2Preamble(message) && bq.Has(message) {
		return
	}

	if block1 != nil {
		if message.Type == CON {
			var (
				ok  bool
				err error
			)
			ok, ls.totalBlocks, ls.bufBlock1, message, err = localStateReceiveARQBlock1(sr, ls.totalBlocks, ls.bufBlock1, message)

			if err != nil {
				fmt.Println("localStateMessageHandlerSelector error", err.Error())
			}

			if ok {
				go respHandler(message, err)
			}
		}
		return
	}

	if block2 != nil {
		switch {
		case message.Type == ACK:
			// подтверждение блока, который отправляем мы (sendARQBlock2ACK)
			id := message.Sender.String() + string(message.Token)
			if c, ok := sr.block2channels.Load(id); ok {
				c.(chan *CoAPMessage) <- message
			}
		case message.Type == CON && bq.Has(message):
			// блок ответа на наш запрос (Server.send): собираем и подтверждаем здесь,
			// receiveARQBlock2 не годится - сокет читает listenLoop, а не он
			var (
				ok  bool
				err error
			)
			ok, ls.totalBlocks2, ls.bufBlock2, message, err = localStateReceiveARQBlock2(sr, ls.totalBlocks2, ls.bufBlock2, message)

			if err != nil {
				fmt.Println("localStateMessageHandlerSelector block2 error", err.Error())
			}

			if ok {
				go respHandler(message, nil)
			}
		}
		return
	}
	go respHandler(message, nil)
}

// isBlock2Preamble - первый пакет block2-передачи большого ответа, см. newACKEmptyMessage
// в sendARQBlock2ACK
func isBlock2Preamble(message *CoAPMessage) bool {
	return message.Type == ACK && message.Code == CoapCodeEmpty &&
		message.GetBlock2() == nil && message.GetOption(OptionSelectiveRepeatWindowSize) != nil
}

// localStateReceiveARQBlock2 собирает block2-ответ на запрос, отправленный через Server.Send.
// Зеркало localStateReceiveARQBlock1 для другой стороны обмена: каждый блок подтверждается
// 2.31 Continue, последний - пустым ACK, по которому отправитель завершает передачу
// (sendARQBlock2ACK выходит на первом ACK с кодом, отличным от Continue).
func localStateReceiveARQBlock2(sr *transport, totalBlocks int, buf map[int][]byte, inputMessage *CoAPMessage) (bool, int, map[int][]byte, *CoAPMessage, error) {
	block := inputMessage.GetBlock2()
	if block == nil || inputMessage.Type != CON {
		return false, totalBlocks, buf, inputMessage, nil
	}

	if !block.MoreBlocks {
		totalBlocks = block.BlockNumber + 1
	}

	buf[block.BlockNumber] = inputMessage.Payload.Bytes()
	if totalBlocks == len(buf) {
		b := assembleBlocks(buf, totalBlocks)
		inputMessage.Payload = NewBytesPayload(b)

		ack := ackTo(nil, inputMessage, CoapCodeEmpty)
		if err := sr.sendToSocketByAddress(ack, inputMessage.Sender); err != nil {
			return false, totalBlocks, buf, inputMessage, err
		}
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
		b := assembleBlocks(buf, totalBlocks)
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
