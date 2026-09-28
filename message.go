package coalago

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A Message object represents a CoAP payload
// CoAPMessage represents a CoAP (Constrained Application Protocol) message.
type CoAPMessage struct {
	MessageID uint16               // MessageID is the unique identifier for the message.
	Type      CoapType             // Type indicates the type of the CoAP message (e.g., Confirmable, Non-confirmable).
	Code      CoapCode             // Code represents the request method or response code.
	Payload   CoAPMessagePayload   // Payload contains the data being transmitted.
	Token     []byte               // Token is used to match responses with requests.
	Options   []*CoAPMessageOption // Options are the optional parameters for the message.

	Sender    net.Addr // Sender is the address of the message sender.
	Recipient net.Addr // Recipient is the address of the message recipient.

	Attempts int           // Attempts is the number of times the message has been sent.
	LastSent time.Time     // LastSent is the timestamp of the last send attempt.
	Timeout  time.Duration // Timeout is the duration to wait for a response before timing out.

	IsProxies bool // IsProxies indicates if the message is being proxied.

	BreakConnectionOnPK func(actualPK []byte) bool // BreakConnectionOnPK is a function to break the connection based on the peer's public key.
	PeerPublicKey       []byte                     // PeerPublicKey is the public key of the peer.

	ProxyAddr string          // ProxyAddr is the address of the proxy server.
	Context   context.Context // Context carries deadlines, cancellation signals, and other request-scoped values.

	// AddChecksumOnSend enables automatic OptionChecksum calculation in the send path.
	AddChecksumOnSend bool
}

func NewCoAPMessage(messageType CoapType, messageCode CoapCode) *CoAPMessage {
	return &CoAPMessage{
		MessageID: generateMessageID(),
		Type:      messageType,
		Code:      messageCode,
		Payload:   NewEmptyPayload(),
		Token:     generateToken(6),
		Timeout:   timeWait,
	}
}

func NewCoAPMessageId(messageType CoapType, messageCode CoapCode, messageID uint16) *CoAPMessage {
	return &CoAPMessage{
		MessageID: messageID,
		Type:      messageType,
		Code:      messageCode,
		Token:     generateToken(6),
		Timeout:   timeWait,
	}
}

// newMessage — NewCoAPMessage с заданным токеном: для служебных сообщений, где токен
// берётся из запроса, и генерировать свой незачем.
func newMessage(messageType CoapType, messageCode CoapCode, token []byte) *CoAPMessage {
	return &CoAPMessage{
		MessageID: generateMessageID(),
		Type:      messageType,
		Code:      messageCode,
		Payload:   NewEmptyPayload(),
		Token:     token,
		Timeout:   timeWait,
	}
}

// newMessageWithID — NewCoAPMessageId с заданным токеном (ответы: ID и токен запроса).
func newMessageWithID(messageType CoapType, messageCode CoapCode, messageID uint16, token []byte) *CoAPMessage {
	return &CoAPMessage{
		MessageID: messageID,
		Type:      messageType,
		Code:      messageCode,
		Token:     token,
		Timeout:   timeWait,
	}
}

// Converts an array of bytes to a Mesasge object.
// An error is returned if a parsing error occurs
func Deserialize(data []byte) (*CoAPMessage, error) {
	m, err := deserialize(data)
	if m == nil && err == nil {
		return nil, ErrNilMessage
	}
	if err != nil {
		MetricBreakedMessages.Inc()
		return m, err
	}
	if err := verifyChecksum(m); err != nil {
		MetricBreakedMessages.Inc()
		return nil, ErrChecksumMismatch
	}
	return m, err
}

// rxMessage — входящее сообщение одной аллокацией: само сообщение, его тело, токен и
// первые rxInlineOptions опций лежат вместе. Прежний разбор делал ~11 аллокаций на
// сообщение и оставлял токен и тело ссылками в буфер чтения, из-за чего буфер нельзя
// было переиспользовать.
type rxMessage struct {
	msg     CoAPMessage
	payload BytesPayload
	tok     [8]byte
	opts    [rxInlineOptions]CoAPMessageOption
	optPtrs [rxInlineOptions]*CoAPMessageOption
	nopts   int
}

const rxInlineOptions = 4

func (r *rxMessage) addOption(code OptionCode, value interface{}) {
	var opt *CoAPMessageOption
	if r.nopts < rxInlineOptions {
		opt = &r.opts[r.nopts]
		opt.Code, opt.Value = code, value
		if r.msg.Options == nil {
			r.msg.Options = r.optPtrs[:0]
		}
	} else {
		opt = NewOption(code, value)
	}
	r.nopts++
	r.msg.Options = append(r.msg.Options, opt)
}

// deserialize разбирает датаграмму. Сообщение не ссылается на data: токен, опции и
// тело скопированы, и буфер чтения можно сразу использовать снова.
func deserialize(data []byte) (*CoAPMessage, error) {
	defer func() {
		recover()
	}()

	dataLen := len(data)
	if dataLen < 4 {
		return &CoAPMessage{}, ErrPacketLengthLessThan4
	}

	ver := data[DataHeader] >> 6
	if ver != 1 {
		return nil, ErrInvalidCoapVersion
	}

	r := &rxMessage{}
	msg := &r.msg

	msg.Type = CoapType(data[DataHeader] >> 4 & 0x03)
	tokenLength := data[DataHeader] & 0x0f
	msg.Code = CoapCode(data[DataCode])

	msg.MessageID = binary.BigEndian.Uint16(data[DataMsgIDStart:DataMsgIDEnd])

	// Token
	if tokenLength > 0 {
		token := data[DataTokenStart : DataTokenStart+tokenLength]
		if int(tokenLength) <= len(r.tok) {
			msg.Token = r.tok[:tokenLength:tokenLength]
			copy(msg.Token, token)
		} else {
			msg.Token = append([]byte(nil), token...)
		}
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
				r.addOption(optCode, intVal)

			case OptionURIHost, OptionEtag, OptionLocationPath, OptionURIPath, OptionURIQuery,
				OptionLocationQuery, OptionProxyURI, OptionСoapsUri, OptionChecksum:
				r.addOption(optCode, string(optionValue))
			default:
				if lastOptionID&0x01 == 1 {
					return msg, ErrUnknownCriticalOption
				}
			}
			tmp = tmp[optionLength:]
		} else {
			r.addOption(optCode, nil)
		}
	}
	// Сообщение не должно делить опции со встроенным массивом через append: добавление
	// опции в клон писало бы в ту же ячейку, что и добавление в оригинал.
	msg.Options = msg.Options[:len(msg.Options):len(msg.Options)]

	r.payload.content = append([]byte{}, tmp...)
	msg.Payload = &r.payload

	err := validateMessage(msg)

	return msg, err
}

// Converts a message object to a byte array. Typically done prior to transmission
//
// Байты на выходе те же, что у прежней реализации (bytes.Buffer, sort.Sort и
// valueToBytes на каждую опцию): размер считается заранее, буфер аллоцируется один раз,
// числа и строки пишутся в него напрямую.
func Serialize(msg *CoAPMessage) ([]byte, error) {
	if option := msg.GetOption(OptionURIScheme); option != nil {
		if option.Value == nil || option.IntValue() != COAPS_SCHEME {
			msg.AddOption(OptionURIScheme, COAP_SCHEME)
		}
	}

	// Sort Options
	sortOptionsStable(msg.Options)

	var (
		payload    []byte
		payloadStr string
		hasPayload bool
	)
	switch p := msg.Payload.(type) {
	case nil:
	case *BytesPayload:
		payload, hasPayload = p.content, len(p.content) > 0
	case *StringCoAPMessagePayload:
		payloadStr, hasPayload = p.content, len(p.content) > 0
	default:
		if p.Length() > 0 {
			payload, hasPayload = p.Bytes(), true
		}
	}

	size := 4 + len(msg.Token)
	lastOptionCode := 0
	for _, opt := range msg.Options {
		delta, length := int(opt.Code)-lastOptionCode, optionValueLen(opt.Value)
		size += 1 + optionExtLen(delta) + optionExtLen(length) + length
		lastOptionCode = int(opt.Code)
	}
	if hasPayload {
		size += 1 + len(payload) + len(payloadStr)
	}

	buf := make([]byte, 0, size)
	buf = append(buf,
		(1<<6)|(uint8(msg.Type)<<4)|0x0f&uint8(len(msg.Token)),
		byte(msg.Code),
		byte(msg.MessageID>>8),
		byte(msg.MessageID),
	)
	buf = append(buf, msg.Token...)

	lastOptionCode = 0
	for _, opt := range msg.Options {
		optCode := int(opt.Code)
		optDelta := optCode - lastOptionCode
		optDeltaValue, _ := getOptionHeaderValue(optDelta)
		optLength := optionValueLen(opt.Value)
		optLengthValue, _ := getOptionHeaderValue(optLength)

		// Option Header
		buf = append(buf, byte(optDeltaValue<<4|optLengthValue))

		// Extended Delta & Length
		if optDeltaValue == 13 {
			buf = append(buf, byte(optDelta-13))
		} else if optDeltaValue == 14 {
			buf = binary.BigEndian.AppendUint16(buf, uint16(optDelta-269))
		}

		if optLengthValue == 13 {
			buf = append(buf, byte(optLength-13))
		} else if optLengthValue == 14 {
			buf = binary.BigEndian.AppendUint16(buf, uint16(optLength-269))
		}

		// Option Value
		buf = appendOptionValue(buf, opt.Value)
		lastOptionCode = optCode
	}

	if hasPayload {
		buf = append(buf, PayloadMarker)
		buf = append(buf, payload...)
		buf = append(buf, payloadStr...)
	}

	return buf, nil
}

func applyChecksum(msg *CoAPMessage) error {
	msg.RemoveOptions(OptionChecksum)

	checksum, err := calculateChecksum(msg)
	if err != nil {
		return err
	}

	msg.AddOption(OptionChecksum, checksum)
	return nil
}

func calculateChecksum(msg *CoAPMessage) (string, error) {
	copyMsg := cloneForChecksum(msg)

	buf, err := Serialize(copyMsg)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(buf)), nil
}

func cloneForChecksum(msg *CoAPMessage) *CoAPMessage {
	copyMsg := &CoAPMessage{
		MessageID: msg.MessageID,
		Type:      msg.Type,
		Code:      msg.Code,
		Token:     append([]byte(nil), msg.Token...),
	}

	if msg.Payload != nil {
		copyMsg.Payload = NewBytesPayload(msg.Payload.Bytes())
	} else {
		copyMsg.Payload = NewEmptyPayload()
	}

	copyMsg.Options = make([]*CoAPMessageOption, 0, len(msg.Options))
	for _, opt := range msg.Options {
		if opt.Code == OptionChecksum {
			continue
		}
		copyMsg.Options = append(copyMsg.Options, &CoAPMessageOption{
			Code:  opt.Code,
			Value: opt.Value,
		})
	}

	return copyMsg
}

func verifyChecksum(msg *CoAPMessage) error {
	option := msg.GetOption(OptionChecksum)
	if option == nil {
		return nil
	}

	expected := option.StringValue()
	computed, err := calculateChecksum(msg)
	if err != nil {
		return err
	}

	if expected != computed {
		return fmt.Errorf("%w: expected %s got %s", ErrChecksumMismatch, expected, computed)
	}

	return nil
}

func (m *CoAPMessage) Clone(includePayload bool) *CoAPMessage {
	cloneMessage := newMessageWithID(m.Type, m.Code, m.MessageID, m.Token)
	cloneMessage.Options = m.Options
	cloneMessage.ProxyAddr = m.ProxyAddr
	cloneMessage.BreakConnectionOnPK = m.BreakConnectionOnPK
	cloneMessage.AddChecksumOnSend = m.AddChecksumOnSend
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
	return string(m.appendURI(nil, host))
}

// appendURI дописывает в dst то же, что возвращает GetURI, без промежуточных строк.
func (m *CoAPMessage) appendURI(dst []byte, host string) []byte {
	dst = append(dst, m.GetSchemeString()...)
	dst = append(dst, "://"...)
	dst = append(dst, host...)
	dst = m.appendURIPath(dst)

	// Query: как GetURIQueryString — опция без "=" (или с пустым ключом) даёт пустой
	// элемент, пустая строка запроса не даёт "?".
	queryStart := len(dst)
	dst = append(dst, '?')
	n := 0
	for _, opt := range m.Options {
		if opt.Code != OptionURIQuery {
			continue
		}
		if n > 0 {
			dst = append(dst, '&')
		}
		n++
		q := opt.StringValue()
		if i := strings.Index(q, "="); i > 0 {
			dst = append(dst, url.QueryEscape(q[:i])...)
			dst = append(dst, '=')
			dst = append(dst, url.QueryEscape(q[i+1:])...)
		}
	}
	if len(dst) == queryStart+1 {
		dst = dst[:queryStart]
	}
	return dst
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
	var buf [128]byte
	return string(m.appendURIPath(buf[:0]))
}

// appendURIPath дописывает "/" и сегменты пути через "/" (нестроковое значение сегмента
// даёт пустой сегмент, как StringValue).
func (m *CoAPMessage) appendURIPath(dst []byte) []byte {
	dst = append(dst, '/')
	n := 0
	for _, opt := range m.Options {
		if opt.Code == OptionURIPath {
			if n > 0 {
				dst = append(dst, '/')
			}
			dst = append(dst, opt.StringValue()...)
			n++
		}
	}
	return dst
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

// GetURIQuery возвращает значение параметра q: как strings.SplitN(v, "=", 2) по каждой
// query-опции, но без аллокаций.
func (m *CoAPMessage) GetURIQuery(q string) string {
	for _, opt := range m.Options {
		if opt.Code != OptionURIQuery {
			continue
		}
		v := opt.StringValue()
		if i := strings.IndexByte(v, '='); i >= 0 && v[:i] == q {
			return v[i+1:]
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

func (m *CoAPMessage) SetChecksum(checksum string) {
	m.AddOption(OptionChecksum, checksum)
}

func (m *CoAPMessage) SetAddChecksumOnSend(enabled bool) {
	m.AddChecksumOnSend = enabled
}

func (m *CoAPMessage) GetChecksum() string {
	return m.GetOptionAsString(OptionChecksum)
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
	return []byte(p.content)
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
 * XML Payload
 * Just a copy of String Payload for now
 */

// Represents a message payload containing XML String
type XMLPayload struct {
	StringCoAPMessagePayload
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

/**
 * JSON Payload
 */

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

// marshaledPayload сериализует JSON-тело один раз: дальше его длину и байты спрашивают
// проверка размера, шифрование и Serialize, и каждый вызов JSONPayload делал бы
// json.Marshal заново. Остальные тела возвращаются как есть.
func marshaledPayload(p CoAPMessagePayload) CoAPMessagePayload {
	if jp, ok := p.(*JSONPayload); ok {
		return NewBytesPayload(jp.Bytes())
	}
	return p
}

func isBigPayload(msg *CoAPMessage) bool {
	return msg.Payload != nil && msg.Payload.Length() > MAX_PAYLOAD_SIZE
}
