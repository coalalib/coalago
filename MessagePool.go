package coalago

import (
	"errors"
	"time"

	m "github.com/coalalib/coalago/message"
)

var (
	ErrMaxAttempts = errors.New("Max attempts")
)

func pendingMessagesReader(coala *Coala, senderPool chan *m.CoAPMessage, acknowledgePool *ackPool) {
	for {
		message := <-senderPool

		if message.Type != m.CON {
			sendToSocket(coala, message, message.Recipient)
			continue
		}

		if !acknowledgePool.IsExists(newPoolID(message.MessageID, message.Token, message.Recipient)) {
			continue
		}

		if time.Since(message.LastSent).Seconds() < 3 {
			senderPool <- message
			continue
		}

		message.Attempts++

		if message.Attempts > 3 {
			coala.Metrics.ExpiredMessages.Inc()
			go acknowledgePool.DoDelete(
				newPoolID(message.MessageID, message.Token, message.Recipient),
				nil,
				ErrMaxAttempts,
			)
			continue
		}

		message.LastSent = time.Now()
		if message.Attempts > 1 {
			coala.Metrics.Retransmissions.Inc()
		}

		sendToSocket(coala, message, message.Recipient)
		senderPool <- message
	}
}
