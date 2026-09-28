package coalago

// Прежняя (до оптимизации) реализация разбора и сериализации — эталон для
// дифференциальных тестов: новая обязана давать те же байты и те же сообщения.
// Скопирована из origin/master (c2b08ce) с переименованием в legacy*.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

func legacyDeserialize(data []byte) (*CoAPMessage, error) {
	defer func() {
		recover()
	}()

	msg := &CoAPMessage{}

	dataLen := len(data)
	if dataLen < 4 {
		return msg, ErrPacketLengthLessThan4
	}

	ver := data[DataHeader] >> 6
	if ver != 1 {
		return nil, ErrInvalidCoapVersion
	}

	msg.Type = CoapType(data[DataHeader] >> 4 & 0x03)
	tokenLength := data[DataHeader] & 0x0f
	msg.Code = CoapCode(data[DataCode])

	msg.MessageID = binary.BigEndian.Uint16(data[DataMsgIDStart:DataMsgIDEnd])

	// Token
	if tokenLength > 0 {
		msg.Token = data[DataTokenStart : DataTokenStart+tokenLength]
	}

	/*
	    0   1   2   3   4   5   6   7
	   +---------------+---------------+
	   |               |               |
	   |  Option Delta | Option Length |   1 byte
	   |               |               |
	   +---------------+---------------+
	   \                               \
	   /         Option Delta          /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /         Option Length         /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /                               /
	   \                               \
	   /         Option Value          /   0 or more bytes
	   \                               \
	   /                               /
	   \                               \
	   +-------------------------------+
	*/
	tmp := data[DataTokenStart+msg.GetTokenLength():]

	lastOptionID := uint16(0)
	for len(tmp) > 0 {
		if tmp[0] == PayloadMarker {
			tmp = tmp[1:]
			break
		}

		optionDelta := uint16(tmp[0] >> 4)
		optionLength := uint16(tmp[0] & 0x0f)

		tmp = tmp[1:]
		switch optionDelta {
		case 13:
			optionDeltaExtended := uint16(tmp[0]) + uint16(13)
			optionDelta = optionDeltaExtended
			tmp = tmp[1:]

		case 14:
			optionDeltaExtended := binary.BigEndian.Uint16(tmp[:2])
			optionDelta = optionDeltaExtended + uint16(269)
			tmp = tmp[2:]

		case 15:
			return msg, ErrOptionDeltaUsesValue15
		}

		lastOptionID += optionDelta

		switch optionLength {
		case 13:
			optionLengthExtended := uint16(tmp[0]) + uint16(13)
			optionLength = optionLengthExtended
			tmp = tmp[1:]

		case 14:
			optionLengthExtended := binary.BigEndian.Uint16(tmp[:2])
			optionLength = optionLengthExtended + uint16(269)
			tmp = tmp[2:]

		case 15:
			return msg, ErrOptionLengthUsesValue15
		}

		optCode := OptionCode(lastOptionID)
		if int(optionLength) <= len(tmp) {
			optionValue := tmp[:optionLength]

			switch optCode {
			case OptionURIScheme, OptionProxyScheme, OptionURIPort, OptionContentFormat, OptionMaxAge, OptionAccept, OptionSize1,
				OptionSize2, OptionBlock1, OptionBlock2, OptionHandshakeType, OptionObserve,
				OptionSessionNotFound, OptionSessionExpired, OptionSelectiveRepeatWindowSize, OptionProxySecurityID:
				// OptionWindowtOffset

				intVal, err := legacyDecodeInt(optionValue)
				if err != nil {
					return nil, err
				}
				msg.Options = append(msg.Options, NewOption(optCode, intVal))

			case OptionURIHost, OptionEtag, OptionLocationPath, OptionURIPath, OptionURIQuery,
				OptionLocationQuery, OptionProxyURI, OptionСoapsUri, OptionChecksum:
				msg.Options = append(msg.Options, NewOption(optCode, string(optionValue)))
			default:
				if lastOptionID&0x01 == 1 {
					return msg, ErrUnknownCriticalOption
				}
			}
			tmp = tmp[optionLength:]
		} else {
			msg.Options = append(msg.Options, NewOption(optCode, nil))
		}
	}

	msg.Payload = NewBytesPayload(tmp)

	err := legacyValidateMessage(msg)

	return msg, err
}

// Converts a message object to a byte array. Typically done prior to transmission
func legacySerialize(msg *CoAPMessage) ([]byte, error) {
	if option := msg.GetOption(OptionURIScheme); option != nil {
		if option.Value == nil || option.IntValue() != COAPS_SCHEME {
			msg.AddOption(OptionURIScheme, COAP_SCHEME)
		}
	}

	messageID := []byte{0, 0}
	binary.BigEndian.PutUint16(messageID, msg.MessageID)

	buf := bytes.Buffer{}
	buf.Write([]byte{(1 << 6) | (uint8(msg.Type) << 4) | 0x0f&uint8(len(msg.Token))})
	buf.Write([]byte{byte(msg.Code)})
	buf.Write([]byte{messageID[0]})
	buf.Write([]byte{messageID[1]})
	buf.Write(msg.Token)

	// Sort Options
	sort.Sort(legacySortOptions(msg.Options))

	lastOptionCode := 0
	for _, opt := range msg.Options {
		optCode := int(opt.Code)
		optDelta := optCode - lastOptionCode
		optDeltaValue, _ := legacyGetOptionHeaderValue(optDelta)
		byteValue := legacyValueToBytes(opt.Value)
		valueLength := len(byteValue)
		optLength := valueLength
		optLengthValue, _ := legacyGetOptionHeaderValue(optLength)

		// Option Header
		buf.Write([]byte{byte(optDeltaValue<<4 | optLengthValue)})

		// Extended Delta & Length
		if optDeltaValue == 13 {
			optDelta -= 13
			buf.Write([]byte{byte(optDelta)})
		} else if optDeltaValue == 14 {
			tmpBuf := new(bytes.Buffer)
			optDelta -= 269
			binary.Write(tmpBuf, binary.BigEndian, uint16(optDelta))
			buf.Write(tmpBuf.Bytes())
		}

		if optLengthValue == 13 {
			optLength -= 13
			buf.Write([]byte{byte(optLength)})
		} else if optLengthValue == 14 {
			tmpBuf := new(bytes.Buffer)
			optLength -= 269
			binary.Write(tmpBuf, binary.BigEndian, uint16(optLength))
			buf.Write(tmpBuf.Bytes())
		}

		// Option Value
		buf.Write(byteValue)
		lastOptionCode = optCode
	}

	if msg.Payload != nil && msg.Payload.Length() > 0 {
		buf.Write([]byte{PayloadMarker})
		buf.Write(msg.Payload.Bytes())
	}

	return buf.Bytes(), nil
}

// type to sort the coap options list (which is mandatory) prior to transmission
type legacySortOptions []*CoAPMessageOption

func (opts legacySortOptions) Len() int {
	return len(opts)
}

func (opts legacySortOptions) Swap(i, j int) {
	opts[i], opts[j] = opts[j], opts[i]

	// Check change order of the pathes option.
	if opts[j].Code == OptionURIPath || opts[i].Code == OptionURIPath {
		for index, v := range opts {
			if v.Code == OptionURIPath && index > j && index < i {
				opts[i], opts[index] = opts[index], opts[i]
				opts[j], opts[index] = opts[index], opts[j]
			}
		}
	}
}

func (opts legacySortOptions) Less(i, j int) bool {
	return opts[i].Code < opts[j].Code
}

func legacyGetOptionHeaderValue(optValue int) (int, error) {
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
func legacyValidateMessage(msg *CoAPMessage) error {
	if msg.Type > 3 {
		return ErrUnknownMessageType
	}

	if msg.GetTokenLength() > 8 {
		return ErrInvalidTokenLength
	}

	// Repeated Unrecognized Options
	for _, opt := range msg.Options {
		opts := msg.GetOptions(opt.Code)

		if len(opts) > 1 {
			if !opts[0].IsRepeatableOption() {
				if opts[0].Code&0x01 == 1 {
					return ErrUnknownCriticalOption
				}
			}
		}
	}

	return nil
}

func legacyValueToBytes(value interface{}) []byte {
	var v uint32

	switch i := value.(type) {
	case string:
		return []byte(i)
	case []byte:
		return i
	case MediaType:
		v = uint32(i)
	case byte:
		v = uint32(i)
	case int:
		v = uint32(i)
	case int32:
		v = uint32(i)
	case uint:
		v = uint32(i)
	case uint32:
		v = i
	default:
		break
	}

	return legacyEncodeInt(v)
}

func legacyDecodeInt(b []byte) (uint32, error) {
	if len(b) > 4 {
		return 0, errors.New("data outside of type")
	}
	tmp := []byte{0, 0, 0, 0}
	copy(tmp[4-len(b):], b)

	return binary.BigEndian.Uint32(tmp), nil
}

func legacyEncodeInt(v uint32) []byte {
	switch {
	case v == 0:
		return nil

	case v < 256:
		return []byte{byte(v)}

	case v < 65536:
		rv := []byte{0, 0}
		binary.BigEndian.PutUint16(rv, uint16(v))
		return rv

	default:
		rv := []byte{0, 0, 0, 0}
		binary.BigEndian.PutUint32(rv, uint32(v))
		return rv
	}
}
