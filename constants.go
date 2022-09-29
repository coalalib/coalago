package coalago

import (
	"errors"
	"time"
)

var (
	timeWait        = time.Second
	maxSendAttempts = 6
	sumTimeAttempts = timeWait*time.Duration(maxSendAttempts) + 100
)

const (
	SESSIONS_POOL_EXPIRATION = time.Second * 60 * 2
	MAX_PAYLOAD_SIZE         = 1024
	DEFAULT_WINDOW_SIZE      = 350
	MTU                      = 1500
)

const PayloadMarker = 0xff

const (
	COAP_SCHEME  = 0
	COAPS_SCHEME = 1
)

type CoapType uint8

const (
	CON CoapType = 0
	NON CoapType = 1
	ACK CoapType = 2
	RST CoapType = 3
)

type CoapMethod uint8

const (
	CoapMethodGet    CoapMethod = 1
	CoapMethodPut    CoapMethod = 2
	CoapMethodPost   CoapMethod = 3
	CoapMethodDelete CoapMethod = 4
)

type CoapCode uint8

const (

	//methods
	GET    CoapCode = 1
	POST   CoapCode = 2
	PUT    CoapCode = 3
	DELETE CoapCode = 4

	// Response
	CoapCodeEmpty    CoapCode = 0
	CoapCodeCreated  CoapCode = 65
	CoapCodeDeleted  CoapCode = 66
	CoapCodeValid    CoapCode = 67
	CoapCodeChanged  CoapCode = 68
	CoapCodeContent  CoapCode = 69
	CoapCodeContinue CoapCode = 95 // (2.31 Continue)

	// Errors
	CoapCodeBadRequest               CoapCode = 128
	CoapCodeUnauthorized             CoapCode = 129
	CoapCodeBadOption                CoapCode = 130
	CoapCodeForbidden                CoapCode = 131
	CoapCodeNotFound                 CoapCode = 132
	CoapCodeMethodNotAllowed         CoapCode = 133
	CoapCodeNotAcceptable            CoapCode = 134
	CoapCodeRequestEntityIncomplete  CoapCode = 136 // (4.08)
	CoapCodeConflict                 CoapCode = 137
	CoapCodePreconditionFailed       CoapCode = 140
	CoapCodeRequestEntityTooLarge    CoapCode = 141
	CoapCodeUnsupportedContentFormat CoapCode = 143
	CoapCodeInternalServerError      CoapCode = 160
	CoapCodeNotImplemented           CoapCode = 161
	CoapCodeBadGateway               CoapCode = 162
	CoapCodeServiceUnavailable       CoapCode = 163
	CoapCodeGatewayTimeout           CoapCode = 164
	CoapCodeProxyingNotSupported     CoapCode = 165
)

func (code CoapCode) String() string {
	switch code {
	case GET:
		return "GET"
	case POST:
		return "POST"
	case PUT:
		return "PUT"
	case DELETE:
		return "DELETE"
	case CoapCodeEmpty:
		return "0 Empty"
	case CoapCodeCreated:
		return "201 Created"
	case CoapCodeDeleted:
		return "202 Deleted"
	case CoapCodeValid:
		return "203 Valid"
	case CoapCodeChanged:
		return "204 Changed"
	case CoapCodeContent:
		return "205 Content"
	case CoapCodeContinue:
		return "231 Continue"
	case CoapCodeBadRequest:
		return "400 Bad Request"
	case CoapCodeUnauthorized:
		return "401 Unauthorized"
	case CoapCodeBadOption:
		return "402 Bad Option"
	case CoapCodeForbidden:
		return "403 Forbidden"
	case CoapCodeNotFound:
		return "404 Not Found"
	case CoapCodeMethodNotAllowed:
		return "405 Method Not Allowed"
	case CoapCodeNotAcceptable:
		return "406 Not Acceptable"
	case CoapCodePreconditionFailed:
		return "412 Precondition Failed"
	case CoapCodeRequestEntityTooLarge:
		return "413 Request Entity Too Large"
	case CoapCodeUnsupportedContentFormat:
		return "415 Unsupported Content Format"
	case CoapCodeInternalServerError:
		return "500 Internal Server Error"
	case CoapCodeNotImplemented:
		return "501 Not Implemented"
	case CoapCodeBadGateway:
		return "502 Bad Gateway"
	case CoapCodeServiceUnavailable:
		return "503 Service Unavailable"
	case CoapCodeGatewayTimeout:
		return "504 Gateway Timeout"
	case CoapCodeProxyingNotSupported:
		return "505 Proxying Not Supported"
	default:
		return "Unknown"
	}
}

func (c CoapCode) Group() string {
	if c > 64 && c < 96 {
		return "2.xx"
	} else if c > 127 && c < 144 {
		return "4.xx"
	} else if c > 159 && c < 166 {
		return "5.xx"
	}
	return "others"
}

func (c *CoapCode) IsRegisteredMethod() bool {
	return (*c > 0 && *c <= 4)
}

func (c *CoapCode) IsCommonError() bool {
	return (*c >= 128 && *c < 160)
}

func (c *CoapCode) IsInternalError() bool {
	return (*c >= 160 && *c <= 165)
}

type MediaType int

const (
	MediaTypeTextPlain                  MediaType = 0
	MediaTypeTextXML                    MediaType = 1
	MediaTypeTextCsv                    MediaType = 2
	MediaTypeTextHTML                   MediaType = 3
	MediaTypeImageGif                   MediaType = 21
	MediaTypeImageJpeg                  MediaType = 22
	MediaTypeImagePng                   MediaType = 23
	MediaTypeImageTiff                  MediaType = 24
	MediaTypeAudioRaw                   MediaType = 25
	MediaTypeVideoRaw                   MediaType = 26
	MediaTypeApplicationLinkFormat      MediaType = 40
	MediaTypeApplicationXML             MediaType = 41
	MediaTypeApplicationOctetStream     MediaType = 42
	MediaTypeApplicationRdfXML          MediaType = 43
	MediaTypeApplicationSoapXML         MediaType = 44
	MediaTypeApplicationAtomXML         MediaType = 45
	MediaTypeApplicationXmppXML         MediaType = 46
	MediaTypeApplicationExi             MediaType = 47
	MediaTypeApplicationFastInfoSet     MediaType = 48
	MediaTypeApplicationSoapFastInfoSet MediaType = 49
	MediaTypeApplicationJSON            MediaType = 50
	MediaTypeApplicationXObitBinary     MediaType = 51
	MediaTypeTextPlainVndOmaLwm2m       MediaType = 1541
	MediaTypeTlvVndOmaLwm2m             MediaType = 1542
	MediaTypeJSONVndOmaLwm2m            MediaType = 1543
	MediaTypeOpaqueVndOmaLwm2m          MediaType = 1544
)

type CoapHandshakeType uint8

const (
	CoapHandshakeTypeClientHello     = 1
	CoapHandshakeTypePeerHello       = 2
	CoapHandshakeTypeClientSignature = 3
	CoapHandshakeTypePeerSignature   = 4
)

type OptionCode int

const (
	OptionIfMatch       OptionCode = 1
	OptionURIHost       OptionCode = 3
	OptionEtag          OptionCode = 4
	OptionIfNoneMatch   OptionCode = 5
	OptionObserve       OptionCode = 6
	OptionURIPort       OptionCode = 7
	OptionLocationPath  OptionCode = 8
	OptionURIPath       OptionCode = 11
	OptionContentFormat OptionCode = 12
	OptionMaxAge        OptionCode = 14
	OptionURIQuery      OptionCode = 15
	OptionAccept        OptionCode = 17
	OptionLocationQuery OptionCode = 20
	OptionBlock2        OptionCode = 23
	OptionBlock1        OptionCode = 27
	OptionSize2         OptionCode = 28
	OptionProxyURI      OptionCode = 35
	OptionProxyScheme   OptionCode = 39
	OptionSize1         OptionCode = 60

	/// URI scheme options specifies scheme to be used for message transmission
	/// See `CoAPMessage.GetScheme()`. Scheme is stored using it's raw value
	OptionURIScheme OptionCode = 2111

	/// Handshake option is used by Coala library to detect handshake CoAP messages
	OptionHandshakeType OptionCode = 3999

	/// Session Not Found option indicates to sender that peer has no active coaps:// session.
	/// Upon receiving the message with this option sender must restart the session
	OptionSessionNotFound OptionCode = 4001

	/// Session expired option indicates that peer's coaps:// session expired
	/// Upon receiving the message with this option sender must restart the session
	OptionSessionExpired OptionCode = 4003

	OptionSelectiveRepeatWindowSize OptionCode = 3001
	OptionProxySecurityID           OptionCode = 3004
	// OptionWindowtOffset             OptionCode = 3012

	OptionСoapsUri OptionCode = 4005
)

// Fragments/parts of a CoAP Message packet
const (
	DataHeader     = 0
	DataCode       = 1
	DataMsgIDStart = 2
	DataMsgIDEnd   = 4
	DataTokenStart = 4
)

var (
	ErrPacketLengthLessThan4         = errors.New("Packet length less than 4 bytes")
	ErrInvalidCoapVersion            = errors.New("Invalid CoAP version. Should be 1.")
	ErrOptionLengthUsesValue15       = errors.New(("Message format error. Option length has reserved value of 15"))
	ErrOptionDeltaUsesValue15        = errors.New(("Message format error. Option delta has reserved value of 15"))
	ErrUnknownMessageType            = errors.New("Unknown message type")
	ErrInvalidTokenLength            = errors.New("Invalid Token Length ( > 8)")
	ErrUnknownCriticalOption         = errors.New("Unknown critical option encountered")
	ErrUnsupportedMethod             = errors.New("Unsupported Method")
	ErrNoMatchingRoute               = errors.New("No matching route found")
	ErrUnsupportedContentFormat      = errors.New("Unsupported Content-Format")
	ErrNoMatchingMethod              = errors.New("No matching method")
	ErrNilMessage                    = errors.New("Message is nil")
	ErrRepeatedMessage               = errors.New("Repeated message")
	ErrNilConn                       = errors.New("Connection object is nil")
	ErrNilAddr                       = errors.New("Address cannot be nil")
	ErrOptionLenghtOutOfRangePackets = errors.New("Option lenght out of range packet")
	ErrUndefinedScheme               = errors.New("undefined scheme")
	ErrMaxAttempts                   = errors.New("max attempts")
	ErrorSessionNotFound             = errors.New("session not found")
	ErrorClientSessionNotFound       = errors.New("client session not found")
	ErrorSessionExpired              = errors.New("session expired")
	ErrorClientSessionExpired        = errors.New("client session expired")
	ErrorHandshake                   = errors.New("error handshake")
	ERR_KEYS_NOT_MATCH               = "Expected and current public keys do not match"
)
