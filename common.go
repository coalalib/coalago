package coalago

import (
	"encoding/binary"
	"errors"
	"math/rand"
	randv2 "math/rand/v2"
	"net"
	"sync/atomic"
)

// currentMessageID monotonically increases; uint16 conversion wraps it naturally.
// A single atomic Add keeps concurrent callers from racing between load and store
// (the old check-then-set pattern could skip or duplicate ids under load).
var currentMessageID = uint32(rand.Intn(65535))

func generateMessageID() uint16 {
	return uint16(atomic.AddUint32(&currentMessageID, 1))
}

// generateToken — случайный токен. math/rand/v2 без общего замка: rand.Read из
// math/rand брал глобальный мьютекс на каждый токен.
func generateToken(l int) []byte {
	token := make([]byte, l)
	for i := 0; i < l; i += 8 {
		v := randv2.Uint64()
		for j := i; j < l && j < i+8; j++ {
			token[j] = byte(v)
			v >>= 8
		}
	}
	return token
}

// sortOptionsStable упорядочивает опции по коду перед отправкой (обязательно в CoAP),
// сохраняя порядок одинаковых опций: сегменты пути и query идут, как их добавили.
// Прежний sort.Sort с доработанным Swap на коротких списках (до 12 опций) сводился
// ровно к этой сортировке вставками, а на длинных мог переставить сегменты пути.
func sortOptionsStable(opts []*CoAPMessageOption) {
	for i := 1; i < len(opts); i++ {
		for j := i; j > 0 && opts[j].Code < opts[j-1].Code; j-- {
			opts[j], opts[j-1] = opts[j-1], opts[j]
		}
	}
}

func getOptionHeaderValue(optValue int) (int, error) {
	switch true {
	case optValue <= 12:
		return optValue, nil

	case optValue <= 268:
		return 13, nil

	case optValue <= 65804:
		return 14, nil
	}
	return 0, errors.New("invalid Option Delta")
}

// Validates a message object and returns any error upon validation failure
func validateMessage(msg *CoAPMessage) error {
	if msg.Type > 3 {
		return ErrUnknownMessageType
	}

	if msg.GetTokenLength() > 8 {
		return ErrInvalidTokenLength
	}

	// Repeated Unrecognized Options: критичная неповторяемая опция встречается дважды.
	for i, opt := range msg.Options {
		if opt.Code&0x01 != 1 || opt.IsRepeatableOption() {
			continue
		}
		for _, other := range msg.Options[i+1:] {
			if other.Code == opt.Code {
				return ErrUnknownCriticalOption
			}
		}
	}

	return nil
}

// optionUint — числовое значение опции для сериализации. Типы, которых здесь нет
// (uint16, int64 и др.), кодируются как 0, то есть пустым значением, — как и раньше.
func optionUint(value interface{}) uint32 {
	switch i := value.(type) {
	case MediaType:
		return uint32(i)
	case byte:
		return uint32(i)
	case int:
		return uint32(i)
	case int32:
		return uint32(i)
	case uint:
		return uint32(i)
	case uint32:
		return i
	}
	return 0
}

// optionValueLen — длина значения опции на проводе.
func optionValueLen(value interface{}) int {
	switch i := value.(type) {
	case string:
		return len(i)
	case []byte:
		return len(i)
	}
	switch v := optionUint(value); {
	case v == 0:
		return 0
	case v < 256:
		return 1
	case v < 65536:
		return 2
	default:
		return 4
	}
}

// appendOptionValue дописывает значение опции: строки и байты как есть, числа —
// минимальным big-endian (0 — пустое значение).
func appendOptionValue(dst []byte, value interface{}) []byte {
	switch i := value.(type) {
	case string:
		return append(dst, i...)
	case []byte:
		return append(dst, i...)
	}
	switch v := optionUint(value); {
	case v == 0:
		return dst
	case v < 256:
		return append(dst, byte(v))
	case v < 65536:
		return binary.BigEndian.AppendUint16(dst, uint16(v))
	default:
		return binary.BigEndian.AppendUint32(dst, v)
	}
}

// optionExtLen — сколько байт занимает расширенная дельта или длина опции.
func optionExtLen(v int) int {
	switch h, _ := getOptionHeaderValue(v); h {
	case 13:
		return 1
	case 14:
		return 2
	}
	return 0
}

func decodeInt(b []byte) (uint32, error) {
	if len(b) > 4 {
		return 0, errors.New("data outside of type")
	}
	tmp := []byte{0, 0, 0, 0}
	copy(tmp[4-len(b):], b)

	return binary.BigEndian.Uint32(tmp), nil
}

func typeString(c CoapType) string {
	switch c {
	case CON:
		return "CON"
	case NON:
		return "NON"
	case ACK:
		return "ACK"
	case RST:
		return "RST"
	}
	return ""
}

func optionCodeToString(option OptionCode) string {
	switch option {
	case OptionIfMatch:
		return "IfMatch"
	case OptionURIHost:
		return "URIHost"
	case OptionEtag:
		return "Etag"
	case OptionIfNoneMatch:
		return "IfNoneMatch"
	case OptionObserve:
		return "Observe"
	case OptionURIPort:
		return "URIPort"
	case OptionLocationPath:
		return "LocationPath"
	case OptionURIPath:
		return "URIPath"
	case OptionContentFormat:
		return "ContentFormat"
	case OptionMaxAge:
		return "MaxAge"
	case OptionURIQuery:
		return "URIQuery"
	case OptionAccept:
		return "Accept"
	case OptionLocationQuery:
		return "LocationQuery"
	case OptionBlock2:
		return "Block2"
	case OptionBlock1:
		return "Block1"
	case OptionSize2:
		return "Size2"
	case OptionProxyURI:
		return "ProxyURI"
	case OptionProxyScheme:
		return "ProxyScheme"
	case OptionSize1:
		return "Size1"
	case OptionURIScheme:
		return "URIScheme"
	case OptionHandshakeType:
		return "HandshakeType"
	case OptionSessionNotFound:
		return "SessionNotFound"
	case OptionSessionExpired:
		return "SessionExpired"
	case OptionSelectiveRepeatWindowSize:
		return "OptionSelectiveRepeatWindowSize"
	// case OptionWindowtOffset:
	// 	return "OptionWindowOffset"
	case OptionСoapsUri:
		return "OptionСoapsUri"
	case OptionProxySecurityID:
		return "OptionSecurityID"
	case OptionChecksum:
		return "Checksum"
	default:
		return "Unknown"
	}
}

func constructNextBlock(blockType OptionCode, s *stateSend) (*CoAPMessage, bool) {
	s.stop = s.start + s.blockSize
	if s.stop > s.lenght {
		s.stop = s.lenght
	}

	blockbyte := s.payload[s.start:s.stop]
	isMore := s.stop < s.lenght

	blockMessage := newBlockingMessage(
		s.origMessage,
		s.origMessage.Recipient,
		blockbyte,
		blockType,
		s.nextNumBlock,
		s.blockSize,
		s.windowsize,
		isMore,
	)

	s.nextNumBlock++
	s.start = s.stop

	blockMessage.CloneOptions(s.origMessage, OptionProxyURI, OptionProxySecurityID)
	blockMessage.ProxyAddr = s.origMessage.ProxyAddr

	return blockMessage, !isMore
}

func ackTo(initMessage *CoAPMessage, origMessage *CoAPMessage, code CoapCode) *CoAPMessage {
	result := newMessage(ACK, code, origMessage.Token)
	result.MessageID = origMessage.MessageID
	result.CloneOptions(origMessage, OptionURIScheme, OptionSelectiveRepeatWindowSize, OptionBlock1, OptionBlock2, OptionProxySecurityID)
	result.Recipient = origMessage.Sender

	if initMessage != nil {
		result.ProxyAddr = initMessage.ProxyAddr
		result.CloneOptions(initMessage, OptionProxyURI)
	}

	return result
}

func ackToWithWindowOffset(initMessage *CoAPMessage, origMessage *CoAPMessage, code CoapCode, windowSize int, blockNumber int, buf map[int][]byte) *CoAPMessage {
	result := newMessage(ACK, code, origMessage.Token)
	result.MessageID = origMessage.MessageID
	result.CloneOptions(origMessage, OptionURIScheme, OptionSelectiveRepeatWindowSize, OptionBlock1, OptionBlock2, OptionProxySecurityID)
	result.Recipient = origMessage.Sender
	if initMessage != nil {
		result.ProxyAddr = initMessage.ProxyAddr
		result.CloneOptions(initMessage, OptionProxyURI)
	}
	return result
}

func newBlockingMessage(
	origMessage *CoAPMessage,
	recipient net.Addr,
	frame []byte,
	optionBlock OptionCode,
	blockNum,
	blockSize,
	windowSize int,
	isMore bool,
) *CoAPMessage {
	msg := newMessage(CON, origMessage.Code, origMessage.Token)
	if origMessage.GetScheme() == COAPS_SCHEME {
		msg.SetSchemeCOAPS()
	}

	msg.AddOption(OptionSelectiveRepeatWindowSize, windowSize)
	msg.Payload = NewBytesPayload(frame)
	msg.SetURIPath(origMessage.GetURIPath())
	msg.AddChecksumOnSend = origMessage.AddChecksumOnSend

	queries := origMessage.GetOptions(OptionURIQuery)
	msg.AddOptions(queries)

	b := newBlock(isMore, blockNum, blockSize)

	msg.AddOption(optionBlock, b.ToInt())
	msg.Recipient = recipient
	msg.ProxyAddr = origMessage.ProxyAddr

	return msg
}

type stateSend struct {
	lenght       int
	start        int
	stop         int
	nextNumBlock int
	blockSize    int
	windowsize   int
	origMessage  *CoAPMessage
	payload      []byte
}

func newACKEmptyMessage(message *CoAPMessage, windowSize int) *CoAPMessage {
	emptyAckMessage := newMessage(ACK, CoapCodeEmpty, message.Token)
	emptyAckMessage.MessageID = message.MessageID
	emptyAckMessage.Code = CoapCodeEmpty
	emptyAckMessage.Recipient = message.Recipient
	emptyAckMessage.Payload = NewEmptyPayload()
	emptyAckMessage.AddOption(OptionSelectiveRepeatWindowSize, windowSize)
	emptyAckMessage.CloneOptions(message, OptionBlock1, OptionBlock2, OptionSelectiveRepeatWindowSize, OptionProxySecurityID)

	return emptyAckMessage
}
