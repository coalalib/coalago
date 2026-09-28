package coalago

import (
	"fmt"
)

// handleMessage обрабатывает входящее сообщение сервера: дедупликация, безопасность,
// сборка блоков и обработчик ресурса. Слот семафора listenLoop освобождается после
// приёма, до обработчика: семафор ограничивает приём, а обработчики, как и раньше, нет.
func (s *Server) handleMessage(message *CoAPMessage, tr *transport, sem chan struct{}) {
	k := messageKey(message)
	if exchanges.processed(k) {
		releaseSlot(sem)
		return
	}

	// Handshake, coaps и блоки Block1 принимаются по одному на обмен, как под прежним
	// мьютексом состояния обмена: повторный ClientHello видит сессию первого, блоки одного
	// запроса собираются по очереди. Разные обмены друг друга не ждут, даже если приём
	// одного встал на записи в сокет. Обычному coap-сообщению (keep-alive /a) сериализация
	// не нужна: при приёме оно не меняет общего состояния.
	var ready *CoAPMessage
	if message.GetScheme() == COAPS_SCHEME || message.GetOption(OptionHandshakeType) != nil || message.GetOption(OptionBlock1) != nil {
		l := exchangeLocks.lock(k)
		ready = acceptMessage(tr, message, k)
		exchangeLocks.unlock(k, l)
	} else {
		ready = acceptMessage(tr, message, k)
	}
	releaseSlot(sem)

	if ready != nil {
		s.respond(ready, tr, k)
	}
}

// acceptMessage — приём сообщения до обработчика: безопасность, блоки Block1 и Block2.
// Возвращает сообщение, готовое к обработке, или nil. Паника при приёме не роняет
// процесс, как и прежде; паника обработчика ресурса — роняет, как и прежде.
func acceptMessage(tr *transport, message *CoAPMessage, k exKey) (ready *CoAPMessage) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("panic in handler: %v\n", r)
			ready = nil
		}
	}()

	if ok, err := localStateSecurityInputLayer(tr, message, ""); !ok || err != nil {
		return nil
	}

	MetricReceivedMessages.Inc()

	if block := message.GetBlock1(); block != nil {
		if message.Type != CON {
			return nil
		}
		return receiveBlock1(tr, message, block, k)
	}

	if message.GetBlock2() != nil {
		if message.Type == ACK {
			// ACK блочного ответа — горутине, которая этот ответ отправляет.
			tr.block2mu.Lock()
			ch := tr.block2[k]
			tr.block2mu.Unlock()
			if ch != nil {
				ch <- message
			}
		}
		return nil
	}

	return message
}

// respond запускает обработчик ресурса ровно один раз на обмен: повторы, пришедшие во
// время работы обработчика и ещё processedTTL после ответа, отбрасываются.
func (s *Server) respond(msg *CoAPMessage, tr *transport, k exKey) {
	if !exchanges.claim(k) {
		return
	}
	defer func() {
		if msg.GetOption(OptionBlock1) != nil {
			blockStates.delete(k)
		}
		exchanges.finish(k)
	}()

	if bq.Has(msg) {
		bq.Write(msg)
		return
	}

	requestOnReceive(s.resourceFor(msg), tr, msg)
}

// receiveBlock1 принимает очередной блок большого запроса (вызывается под замком
// обмена). Возвращает собранное сообщение, когда пришли все блоки, иначе отвечает
// Continue на блок.
func receiveBlock1(tr *transport, message *CoAPMessage, block *block, k exKey) *CoAPMessage {
	st := blockStates.get(k)
	if st.assembled != nil {
		return st.assembled
	}

	if !block.MoreBlocks {
		st.totalBlocks = block.BlockNumber + 1
	}
	st.blocks[block.BlockNumber] = message.Payload.Bytes()
	if st.totalBlocks == len(st.blocks) {
		message.Payload = NewBytesPayload(assembleBlocks(st.blocks, st.totalBlocks))
		st.assembled = message
		st.blocks = nil
		return message
	}

	var ack *CoAPMessage
	if w := message.GetOption(OptionSelectiveRepeatWindowSize); w != nil {
		ack = ackToWithWindowOffset(nil, message, CoapCodeContinue, w.IntValue(), block.BlockNumber, st.blocks)
	} else {
		ack = ackTo(nil, message, CoapCodeContinue)
	}

	if err := tr.sendToSocketByAddress(ack, message.Sender); err != nil {
		fmt.Println("localStateMessageHandlerSelector error", err.Error())
	}
	return nil
}

func localStateSecurityInputLayer(tr *transport, message *CoAPMessage, proxyAddr string) (bool, error) {
	if len(proxyAddr) > 0 {
		proxyID, ok := getProxyIDIfNeed(proxyAddr, tr.localAddr())
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
