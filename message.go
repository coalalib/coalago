package newcoala

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Message object represents a CoAP payload
type CoAPMessage struct {
	MessageID uint16
	Type      CoapType
	Code      CoapCode
	Payload   CoAPMessagePayload
	Token     []byte
	Options   []*CoAPMessageOption

	Sender    net.Addr
	Recipient net.Addr

	Attempts int
	LastSent time.Time

	IsProxies bool

	BreakConnectionOnPK func(actualPK []byte) bool
	PeerPublicKey       []byte

	ProxyAddr string
	Context   context.Context
}

func NewCoAPMessage(messageType CoapType, messageCode CoapCode) *CoAPMessage {
	return &CoAPMessage{
		MessageID: generateMessageID(),
		Type:      messageType,
		Code:      messageCode,
		Payload:   NewEmptyPayload(),
		Token:     generateToken(6),
	}
}

func NewCoAPMessageId(messageType CoapType, messageCode CoapCode, messageID uint16) *CoAPMessage {
	return &CoAPMessage{
		MessageID: messageID,
		Type:      messageType,
		Code:      messageCode,
		Token:     generateToken(6),
	}
}

// Converts an array of bytes to a Mesasge object.
// An error is returned if a parsing error occurs
func Deserialize(data []byte) (*CoAPMessage, error) {
	m, err := deserialize(data)
	if m == nil && err == nil {
		return nil, ErrNilMessage
	}
	return m, err
}

func deserialize(data []byte) (*CoAPMessage, error) {
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

				intVal, err := decodeInt(optionValue)
				if err != nil {
					return nil, err
				}
				msg.Options = append(msg.Options, NewOption(optCode, intVal))

			case OptionURIHost, OptionEtag, OptionLocationPath, OptionURIPath, OptionURIQuery,
				OptionLocationQuery, OptionProxyURI, OptionСoapsUri:
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

	err := validateMessage(msg)

	return msg, err
}

// Converts a message object to a byte array. Typically done prior to transmission
func Serialize(msg *CoAPMessage) ([]byte, error) {
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
	sort.Sort(sortOptions(msg.Options))

	lastOptionCode := 0
	for _, opt := range msg.Options {
		optCode := int(opt.Code)
		optDelta := optCode - lastOptionCode
		optDeltaValue, _ := getOptionHeaderValue(optDelta)
		byteValue := valueToBytes(opt.Value)
		valueLength := len(byteValue)
		optLength := valueLength
		optLengthValue, _ := getOptionHeaderValue(optLength)

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

func (m *CoAPMessage) Clone(includePayload bool) *CoAPMessage {
	cloneMessage := NewCoAPMessageId(m.Type, m.Code, m.MessageID)
	cloneMessage.Token = m.Token
	cloneMessage.Options = m.Options
	cloneMessage.ProxyAddr = m.ProxyAddr
	cloneMessage.BreakConnectionOnPK = m.BreakConnectionOnPK
	if includePayload {
		cloneMessage.Payload = m.Payload
	}
	return cloneMessage
}

func (m *CoAPMessage) GetScheme() int {
	option := m.GetOption(OptionURIScheme)
	if option != nil && option.Value != nil && option.IntValue() == COAPS_SCHEME {
		return COAPS_SCHEME
	}
	return COAP_SCHEME
}

func (m *CoAPMessage) GetSchemeString() string {
	option := m.GetOption(OptionURIScheme)
	if option != nil && option.Value != nil && option.IntValue() == COAPS_SCHEME {
		return "coaps"
	}
	return "coap"
}

func (m *CoAPMessage) GetURI(host string) string {
	result := m.GetSchemeString() + "://" + host + m.GetURIPath()
	query := m.GetURIQueryString()
	if len(query) > 0 {
		result += "?" + query
	}
	return result
}

func (m *CoAPMessage) GetMethod() CoapMethod {
	switch m.Code {
	case GET:
		return CoapMethodGet
	case POST:
		return CoapMethodPost
	case PUT:
		return CoapMethodPut
	case DELETE:
		return CoapMethodDelete
	default:
		return 0
	}
}

func (m *CoAPMessage) GetURIHost() string {
	option := m.GetOption(OptionURIHost)

	if option == nil {
		return "localhost"
	}

	return option.StringValue()
}

func (m *CoAPMessage) GetURIPort() int {
	option := m.GetOption(OptionURIPort)

	if option == nil {
		return 0
	}

	return option.IntValue()
}

func (m *CoAPMessage) GetURIPath() string {
	opts := m.GetOptionsAsString(OptionURIPath)

	return "/" + strings.Join(opts, "/")
}

func (m *CoAPMessage) GetURIQueryString() string {
	options := m.GetOptions(OptionURIQuery)

	var query []string
	for _, v := range options {
		query = append(query, parseOneQuery(v.StringValue()))
	}

	return strings.Join(query, "&")
}

func parseOneQuery(q string) string {
	index := strings.Index(q, "=")
	if index > 0 {
		return url.QueryEscape(q[:index]) + "=" + url.QueryEscape(q[index+1:])
	}
	return ""
}

func (m *CoAPMessage) GetURIQueryArray() []string {
	options := m.GetOptions(OptionURIQuery)

	var query []string
	for _, v := range options {
		query = append(query, v.StringValue())
	}

	return query
}

func (m *CoAPMessage) GetURIQuery(q string) string {
	qs := m.GetURIQueryArray()

	for _, v := range qs {
		kv := strings.SplitN(v, "=", 2)
		if len(kv) == 2 {
			if kv[0] == q {
				return kv[1]
			}
		}
	}

	return ""
}

func (m *CoAPMessage) GetCodeString() string {
	codeClass := string(m.Code >> 5)
	codeDetail := string(m.Code & 0x1f)

	return codeClass + "." + codeDetail
}

func (m *CoAPMessage) GetTokenLength() uint8 {
	return uint8(len(m.Token))
}

func (m *CoAPMessage) GetTokenString() string {
	return string(m.Token)
}

func (m *CoAPMessage) GetMessageIDString() string {
	return strconv.Itoa(int(m.MessageID))
}

func (m *CoAPMessage) GetPayload() []byte {
	return m.Payload.Bytes()
}

func (m *CoAPMessage) SetProxy(scheme, addr string) {
	m.ProxyAddr = addr
	m.AddOption(OptionProxyURI, scheme+"://"+addr)
}

func (m *CoAPMessage) SetMediaType(mt MediaType) {
	m.AddOption(OptionContentFormat, mt)
}

func (m *CoAPMessage) SetStringPayload(s string) {
	m.Payload = NewStringPayload(s)
}

func (m *CoAPMessage) SetURIPath(fullPath string) {
	pathParts := strings.Split(fullPath, "/")

	for _, path := range pathParts {
		if path != "" {
			m.AddOption(OptionURIPath, path)
		}
	}
}

func (m *CoAPMessage) SetURIQuery(k, v string) {
	m.AddOption(OptionURIQuery, k+"="+v)
}

func (m *CoAPMessage) SetToken(t string) {
	m.Token = []byte(t)
}

func (message *CoAPMessage) SetSchemeCOAP() {
	message.AddOption(OptionURIScheme, COAP_SCHEME)
}
func (message *CoAPMessage) SetSchemeCOAPS() {
	message.AddOption(OptionURIScheme, COAPS_SCHEME)
}

func (m *CoAPMessage) IsRequest() bool {
	return m.Type == CON
}

func (m *CoAPMessage) ToReadableString() string {
	options := ""
	for _, option := range m.Options {
		options += fmt.Sprintf("%v: '%v' ", optionCodeToString(option.Code), option.Value)
		if option.Code == OptionBlock1 || option.Code == OptionBlock2 {
			block := newBlockFromInt(option.IntValue())
			options += fmt.Sprintf(" [%v | %v | %v]", block.BlockNumber, block.BlockSize, block.MoreBlocks)
		}
	}

	return fmt.Sprintf(
		"%v\t%v\t%v\t%x\t%v\t[%v]",
		typeString(m.Type),
		m.Code.String(),
		m.GetSchemeString(),
		m.Token,
		m.MessageID,
		options)
}

func (m *CoAPMessage) GetProxyKeyReceiver() string {
	return m.GetTokenString() + m.Sender.String()
}

func (m *CoAPMessage) GetProxyKeySender(address net.Addr) string {
	return m.GetTokenString() + address.String()
}

func (m *CoAPMessage) GetACKKeyForSend(address net.Addr) string {
	return address.String() + m.GetTokenString() + m.GetMessageIDString()
}
func (m *CoAPMessage) GetACKKeyForReceive() string {
	return m.Sender.String() + m.GetTokenString() + m.GetMessageIDString()
}

func (m *CoAPMessage) IsProxied() bool {
	return m.GetOption(OptionProxyURI) != nil
}

func (m *CoAPMessage) GetBlock1() *block {
	optionBlock1 := m.GetOption(OptionBlock1)
	if optionBlock1 != nil {
		return newBlockFromInt(optionBlock1.IntValue())
	}
	return nil
}

func (m *CoAPMessage) GetBlock2() *block {
	optionBlock2 := m.GetOption(OptionBlock2)
	if optionBlock2 != nil {
		return newBlockFromInt(optionBlock2.IntValue())
	}
	return nil
}

func ParseQuery(query string) (values map[string][]string) {
	values, _ = url.ParseQuery(query)
	return values
}

// Represents the payload/content of a CoAP Message
type CoAPMessagePayload interface {
	Bytes() []byte
	Length() int
	String() string
}

/**
 * String plain text Payload
 * The most common
 */

// Instantiates a new message payload of type string
func NewStringPayload(s string) CoAPMessagePayload {
	return &StringCoAPMessagePayload{
		content: s,
	}
}

// Represents a message payload containing string value
type StringCoAPMessagePayload struct {
	content string
}

func (p *StringCoAPMessagePayload) Bytes() []byte {
	return bytes.NewBufferString(p.content).Bytes()
}
func (p *StringCoAPMessagePayload) Length() int {
	return len(p.content)
}
func (p *StringCoAPMessagePayload) String() string {
	return p.content
}

/**
 * Bytes Payload
 */

// Represents a message payload containing an array of bytes
func NewBytesPayload(v []byte) CoAPMessagePayload {
	if v == nil {
		v = []byte{}
	}
	return &BytesPayload{
		content: v,
	}
}

type BytesPayload struct {
	content []byte
}

func (p *BytesPayload) Bytes() []byte {
	return p.content
}
func (p *BytesPayload) Length() int {
	return len(p.content)
}
func (p *BytesPayload) String() string {
	return string(p.content)
}

/**
 * Empty Payload
 * Just a stub
 */

func NewEmptyPayload() CoAPMessagePayload {
	return &EmptyPayload{}
}

// Represents an empty message payload
type EmptyPayload struct{}

func (p *EmptyPayload) Bytes() []byte {
	return []byte{}
}
func (p *EmptyPayload) Length() int {
	return 0
}
func (p *EmptyPayload) String() string {
	return ""
}

func NewJSONPayload(obj interface{}) CoAPMessagePayload {
	return &JSONPayload{
		obj: obj,
	}
}

// Represents a message payload containing JSON String
type JSONPayload struct {
	obj interface{}
}

func (p *JSONPayload) Bytes() []byte {
	o, err := json.Marshal(p.obj)
	if err != nil {
		return []byte{}
	}
	return o
}
func (p *JSONPayload) Length() int {
	return len(p.Bytes())
}
func (p *JSONPayload) String() string {
	return string(p.Bytes())
}
