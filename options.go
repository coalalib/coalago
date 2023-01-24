package newcoala

import (
	"strconv"
)

// Represents an Option for a CoAP Message
type CoAPMessageOption struct {
	Code  OptionCode
	Value interface{}
}

// Determines if an option is elective
func (o *CoAPMessageOption) IsElective() bool {
	return int(o.Code)%2 == 0
}

// Determines if an option is critical
func (o *CoAPMessageOption) IsCritical() bool {
	return int(o.Code)%2 != 0
}

// Returns the string value of an option
func (o *CoAPMessageOption) StringValue() string {
	if str, ok := o.Value.(string); ok {
		return str
	}
	return ""
}

func (o *CoAPMessageOption) IntValue() int {

	if o.Value == nil {
		return 0
	}

	switch o.Value.(type) {
	case int:
		return o.Value.(int)
	case int8:
		return int(o.Value.(int8))
	case int16:
		return int(o.Value.(int16))
	case int32:
		return int(o.Value.(int32))
	case uint:
		return int(o.Value.(uint))
	case uint8:
		return int(o.Value.(uint8))
	case uint16:
		return int(o.Value.(uint16))
	case uint32:
		return int(o.Value.(uint32))
	case string:
		intVal, err := strconv.Atoi(o.Value.(string))
		if err != nil {
			return 0
		}
		return intVal
	default:
		return 0
	}
}

func (o *CoAPMessageOption) Uint32Value() uint32 {

	if o.Value == nil {
		return 0
	}

	switch o.Value.(type) {
	case int:
		return uint32(o.Value.(int))
	case int8:
		return uint32(o.Value.(int8))
	case int16:
		return uint32(o.Value.(int16))
	case int32:
		return uint32(o.Value.(int32))
	case uint:
		return uint32(o.Value.(uint))
	case uint8:
		return uint32(o.Value.(uint8))
	case uint16:
		return uint32(o.Value.(uint16))
	case uint32:
		return o.Value.(uint32)
	case string:
		intVal, err := strconv.Atoi(o.Value.(string))
		if err != nil {
			return 0
		}
		return uint32(intVal)
	default:
		return 0
	}
}

func (o *CoAPMessageOption) Uint16Value() uint16 {

	if o.Value == nil {
		return 0
	}

	switch o.Value.(type) {
	case int:
		return uint16(o.Value.(int))
	case int8:
		return uint16(o.Value.(int8))
	case int16:
		return uint16(o.Value.(int16))
	case int32:
		return uint16(o.Value.(int32))
	case uint:
		return uint16(o.Value.(uint))
	case uint8:
		return uint16(o.Value.(uint8))
	case uint16:
		return o.Value.(uint16)
	case uint32:
		return uint16(o.Value.(uint32))

	case string:
		intVal, err := strconv.Atoi(o.Value.(string))
		if err != nil {
			return 0
		}
		return uint16(intVal)
	default:
		return 0
	}
}

// Instantiates a New Option
func NewOption(optionNumber OptionCode, optionValue interface{}) *CoAPMessageOption {
	return &CoAPMessageOption{
		Code:  optionNumber,
		Value: optionValue,
	}
}

// Checks if an option is repeatable
func (opt *CoAPMessageOption) IsRepeatableOption() bool {
	switch opt.Code {
	case OptionIfMatch, OptionEtag, OptionLocationPath, OptionURIPath, OptionURIQuery, OptionLocationQuery:
		return true
	default:
		return false
	}
}

// Checks if an option/option code is recognizable/valid
func (opt *CoAPMessageOption) IsValidOption() bool {
	switch opt.Code {
	case OptionIfNoneMatch, OptionURIScheme, OptionURIHost,
		OptionEtag, OptionIfMatch, OptionObserve, OptionURIPort, OptionLocationPath,
		OptionURIPath, OptionContentFormat, OptionMaxAge, OptionURIQuery, OptionAccept,
		OptionLocationQuery, OptionBlock2, OptionBlock1, OptionProxyURI, OptionProxySecurityID, OptionProxyScheme, OptionSize1,
		OptionHandshakeType, OptionSessionNotFound, OptionSessionExpired, OptionSelectiveRepeatWindowSize:
		// OptionWindowtOffset
		return true
	default:
		return false
	}
}

// Returns an array of options given an option code
func (m *CoAPMessage) GetOptions(id OptionCode) []*CoAPMessageOption {
	var opts []*CoAPMessageOption
	for _, val := range m.Options {
		if val.Code == id {
			opts = append(opts, val)
		}
	}
	return opts
}

// Returns the first option found for a given option code
func (m *CoAPMessage) GetOption(id OptionCode) *CoAPMessageOption {
	for _, val := range m.Options {
		if val.Code == id {
			return val
		}
	}
	return nil
}

func (m *CoAPMessage) GetOptionAsString(id OptionCode) (str string) {
	if opt := m.GetOption(id); opt != nil {
		return opt.StringValue()
	}
	return
}

// Attempts to return the string value of an Option
func (m *CoAPMessage) GetOptionsAsString(id OptionCode) (str []string) {
	opts := m.GetOptions(id)
	for _, o := range opts {
		str = append(str, o.StringValue())
	}
	return
}

func (m *CoAPMessage) GetOptionProxyURIasString() string {
	return m.GetOptionAsString(OptionProxyURI)
}

func (m *CoAPMessage) GetOptionProxySchemeAsString() string {
	s := m.GetOptionProxyScheme()
	switch s {
	case COAP_SCHEME:
		return "coap"
	case COAPS_SCHEME:
		return "coaps"
	}
	return ""
}

func (m *CoAPMessage) GetOptionProxyScheme() int {
	opt := m.GetOption(OptionProxyScheme)
	if opt == nil {
		return -1
	}
	return opt.IntValue()
}

// Add an Option to the message. If an option is not repeatable, it will replace
// any existing defined Option of the same type
func (m *CoAPMessage) AddOption(code OptionCode, value interface{}) {
	opt := NewOption(code, value)
	if opt.IsRepeatableOption() {
		m.Options = append(m.Options, opt)
	} else {
		m.RemoveOptions(code)
		m.Options = append(m.Options, opt)
	}
}

// Add an array of Options to the message. If an option is not repeatable, it will replace
// any existing defined Option of the same type
func (m *CoAPMessage) AddOptions(opts []*CoAPMessageOption) {
	for _, opt := range opts {
		m.AddOption(opt.Code, opt.Value)
	}
}

// Copies the given list of options from another message to this one
func (m *CoAPMessage) CloneOptions(cm *CoAPMessage, opts ...OptionCode) {
	for _, opt := range opts {
		m.AddOptions(cm.GetOptions(opt))
	}
}

// Removes an Option
func (m *CoAPMessage) RemoveOptions(id OptionCode) {
	var opts []*CoAPMessageOption
	for _, opt := range m.Options {
		if opt.Code != id {
			opts = append(opts, opt)
		}
	}
	m.Options = opts
}
