package newcoala

import (
	"errors"
	"math"
	"net"
	"sync"
	"time"
)

var (
	ErrMaxAttempts = errors.New("max attempts")
)

type Server struct {
	privateKey []byte

	postResources   map[string]CoAPResourceHandler
	getResources    map[string]CoAPResourceHandler
	deleteResources map[string]CoAPResourceHandler

	block2sendsMX sync.RWMutex
	block2sends   map[string]chan *CoAPMessage

	block1receiveMX sync.RWMutex
	block1receive   map[string]chan *CoAPMessage

	inProcessMX sync.RWMutex
	inProcess   map[string]struct{}

	secSessions *securitySessionStorage
}

func NewServer(pk []byte) *Server {
	s := new(Server)
	s.postResources = make(map[string]CoAPResourceHandler)
	s.getResources = make(map[string]CoAPResourceHandler)
	s.deleteResources = make(map[string]CoAPResourceHandler)

	s.block2sends = make(map[string]chan *CoAPMessage)
	s.block1receive = make(map[string]chan *CoAPMessage)

	s.inProcess = make(map[string]struct{})
	s.secSessions = newSecuritySessionStorage()

	s.privateKey = pk
	return s
}

func (s *Server) Listen(addr string) (err error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()

	for {
		buf := make([]byte, 1500)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		msg, err := buildMsg(addr, buf[:n])
		if err != nil {
			continue
		}

		MetricReceivedMessages.Inc()
		s.serve(pc, msg)
	}
}

func (s *Server) AddGETResource(path string, handler CoAPResourceHandler) {
	s.getResources[path] = handler
}

func (s *Server) AddPOSTResource(path string, handler CoAPResourceHandler) {
	s.postResources[path] = handler

}

func (s *Server) AddDELETEResource(path string, handler CoAPResourceHandler) {
	s.deleteResources[path] = handler
}

func buildMsg(addr net.Addr, buf []byte) (*CoAPMessage, error) {
	message, err := Deserialize(buf)
	if err != nil {
		return nil, err
	}
	if message == nil {
		return nil, ErrNilMessage
	}

	message.Sender = addr

	return message, nil
}

func (s *Server) serve(pc net.PacketConn, msg *CoAPMessage) {
	next, err := s.securityInputLayer(pc, s.privateKey, msg)
	if !next || err != nil {
		s.deleteInProcess(msg.GetTokenString())
		return
	}

	switch msg.Type {
	case NON, CON:
		if block := msg.GetBlock1(); block != nil {
			s.block1receiveMX.Lock()
			ch, ok := s.block1receive[msg.GetTokenString()]

			if !ok {
				ch = make(chan *CoAPMessage, 1)
				s.block1receive[msg.GetTokenString()] = ch
				go s.receiveARQBlock1(pc, msg, ch)
			}
			s.block1receiveMX.Unlock()

			ch <- msg
			return
		}

		s.serveCON(pc, msg)
	case ACK:
		s.serveACK(pc, msg)
	}
}

func (s *Server) serveCON(pc net.PacketConn, msg *CoAPMessage) {
	if res, ok := s.getResource(msg); ok {
		s.inProcessMX.Lock()

		_, ok := s.inProcess[msg.GetTokenString()]
		if ok {
			s.inProcessMX.Unlock()
			return
		}

		s.inProcess[msg.GetTokenString()] = struct{}{}
		s.inProcessMX.Unlock()

		go func() {
			s.resourceProcessor(pc, msg, res)
			s.deleteInProcess(msg.GetTokenString())
		}()
	}
}

func (s *Server) serveACK(pc net.PacketConn, msg *CoAPMessage) {
	if block := msg.GetBlock2(); block != nil {

		s.block2sendsMX.RLock()
		ch, ok := s.block2sends[msg.GetTokenString()]

		if ok {
			ch <- msg
		}
		s.block2sendsMX.RUnlock()

	}
}

func (s *Server) getResource(msg *CoAPMessage) (res CoAPResourceHandler, ok bool) {
	path := msg.GetURIPath()
	switch msg.Code {
	case POST:
		res, ok = s.postResources[path]
	case GET:
		res, ok = s.getResources[path]
	case DELETE:
		res, ok = s.deleteResources[path]
	}

	return
}

func (s *Server) resourceProcessor(pc net.PacketConn, msg *CoAPMessage, res CoAPResourceHandler) {
	result := res(msg)
	if msg.Type == NON {
		return
	}

	// Create ACK response with the same ID and given reponse Code
	responseMessage := NewCoAPMessageId(ACK, result.Code, msg.MessageID)
	if result.Payload != nil {
		responseMessage.Payload = result.Payload
	} else {
		responseMessage.Payload = NewEmptyPayload()
	}

	// Replicate Token of the original message if any
	responseMessage.Token = msg.Token

	// Setup additional Content Format description if necessary
	if result.MediaType >= 0 {
		responseMessage.AddOption(OptionContentFormat, result.MediaType)
	}

	// validate Observe option (add Option in Response upon registration!)
	if option := msg.GetOption(OptionObserve); option != nil && option.IntValue() == 0 {
		responseMessage.AddOption(OptionObserve, 1)
	}

	// Validate message scheme
	if msg.GetScheme() == COAPS_SCHEME {
		responseMessage.SetSchemeCOAPS()
	}

	responseMessage.CloneOptions(msg, OptionBlock1, OptionBlock2, OptionSelectiveRepeatWindowSize, OptionProxySecurityID)
	if responseMessage.Payload.Length() > MAX_PAYLOAD_SIZE {
		s.sendBlock2Response(pc, responseMessage, msg.Sender)
	} else {
		s.send(pc, responseMessage, msg.Sender)
	}
}

type packet struct {
	acked    bool
	attempts int
	lastSend time.Time
	message  *CoAPMessage
}

func (s *Server) deleteInProcess(token string) {
	s.inProcessMX.Lock()
	delete(s.inProcess, token)
	s.inProcessMX.Unlock()
}

func (s *Server) sendBlock2Response(pc net.PacketConn, sendsMessage *CoAPMessage, addr net.Addr) {
	ch := make(chan *CoAPMessage, 1024)

	s.block2sendsMX.Lock()
	s.block2sends[sendsMessage.GetTokenString()] = ch
	s.block2sendsMX.Unlock()

	defer func() {
		s.block2sendsMX.Lock()
		delete(s.block2sends, sendsMessage.GetTokenString())
		close(ch)
		s.block2sendsMX.Unlock()
	}()

	packets := []*packet{}
	state := makeState(sendsMessage)

	emptyAckMessage := newACKEmptyMessage(sendsMessage, state.windowsize)
	if err := s.send(pc, emptyAckMessage, addr); err != nil {
		return
	}

	for {
		blockMessage, end := constructNextBlock(OptionBlock2, state)
		packets = append(packets, &packet{
			acked:   false,
			message: blockMessage,
		})

		if end {
			break
		}
	}

	shift := 0

	if err := s.sendPacketsToAddr(pc, packets, state.windowsize, shift, addr); err != nil {
		return
	}

	for {
		select {
		case <-time.After(sumTimeAttempts):
			return
		case resp := <-ch:
			block := resp.GetBlock2()
			if len(packets) < block.BlockNumber {
				continue
			}

			if resp.Code != CoapCodeContinue {
				return
			}

			if block.BlockNumber >= len(packets) {
				continue
			}

			packets[block.BlockNumber].acked = true
			if block.BlockNumber != shift {
				continue
			}

			shift++

			for _, p := range packets[shift:] {
				if p.acked {
					shift++
				} else {
					break
				}
			}

			if err := s.sendPacketsToAddr(pc, packets, state.windowsize, shift, addr); err != nil {
				return
			}
		}
	}
}

func makeState(msg *CoAPMessage) *stateSend {
	state := new(stateSend)
	state.payload = msg.Payload.Bytes()
	state.lenght = len(state.payload)
	state.origMessage = msg
	state.blockSize = MAX_PAYLOAD_SIZE
	numblocks := math.Ceil(float64(state.lenght) / float64(MAX_PAYLOAD_SIZE))
	if numblocks < DEFAULT_WINDOW_SIZE {
		state.windowsize = int(numblocks)
	} else {
		state.windowsize = DEFAULT_WINDOW_SIZE
	}

	return state
}

func (s *Server) sendPacketsToAddr(pc net.PacketConn, packets []*packet, windowsize int, shift int, addr net.Addr) error {
	stop := shift + windowsize
	if stop >= len(packets) {
		stop = len(packets)
	}

	if shift == len(packets) {
		return ErrMaxAttempts
	}

	for i := 0; i < stop; i++ {
		if packets[i].acked {
			continue
		}

		if time.Since(packets[i].lastSend) < timeWait {
			continue
		}

		if packets[i].attempts == maxSendAttempts {
			MetricExpiredMessages.Inc()
			return ErrMaxAttempts
		}
		packets[i].attempts++
		if packets[i].attempts > 1 {
			MetricRetransmitMessages.Inc()
		}
		packets[i].lastSend = time.Now()
		m := packets[i].message

		if err := s.send(pc, m, addr); err != nil {
			return err
		}

	}

	return nil
}

func (s *Server) send(pc net.PacketConn, m *CoAPMessage, addr net.Addr) error {
	if err := s.securityOutputLayer(pc, m, addr); err == nil {
		if b, err := Serialize(m); err == nil {
			MetricSentMessages.Inc()
			pc.WriteTo(b, addr)
		}
	} else {
		return err
	}

	return nil
}

func (s *Server) deleteBlock1Receive(token string, c chan *CoAPMessage) {
	s.block1receiveMX.Lock()
	delete(s.block1receive, token)
	close(c)
	s.block1receiveMX.Unlock()
}

func (s *Server) receiveARQBlock1(pc net.PacketConn, msg *CoAPMessage, input chan *CoAPMessage) {
	var (
		fullmsg     *CoAPMessage
		buf         = make(map[int][]byte)
		totalBlocks = -1
	)

	for {
		select {
		case inputMessage := <-input:
			block := inputMessage.GetBlock1()
			if block == nil || inputMessage.Type != CON {
				continue
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
				fullmsg = inputMessage
				s.deleteBlock1Receive(msg.GetTokenString(), input)
				break
			}

			var ack *CoAPMessage
			w := inputMessage.GetOption(OptionSelectiveRepeatWindowSize)
			if w != nil {
				ack = ackToWithWindowOffset(nil, inputMessage, CoapCodeContinue, w.IntValue(), block.BlockNumber, buf)
			} else {
				ack = ackTo(nil, inputMessage, CoapCodeContinue)
			}

			if err := s.send(pc, ack, inputMessage.Sender); err != nil {
				s.deleteBlock1Receive(msg.GetTokenString(), input)
				return
			}

		case <-time.After(sumTimeAttempts):
			s.deleteBlock1Receive(msg.GetTokenString(), input)
			return
		}

		if fullmsg != nil {
			break
		}
	}
	s.serveCON(pc, fullmsg)
}
