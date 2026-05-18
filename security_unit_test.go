package coalago

import (
	"bytes"
	"testing"
)

func TestNewClientHelloMessageUsesGet(t *testing.T) {
	publicKey := []byte{0x01, 0x02, 0x03}
	orig := NewCoAPMessage(CON, POST)

	message := newClientHelloMessage(orig, publicKey)

	if message.Type != CON {
		t.Fatalf("Type = %v, want %v", message.Type, CON)
	}
	if message.Code != GET {
		t.Fatalf("Code = %v, want %v", message.Code, GET)
	}
	if option := message.GetOption(OptionHandshakeType); option == nil || option.IntValue() != CoapHandshakeTypeClientHello {
		t.Fatalf("HandshakeType = %#v, want ClientHello", option)
	}
	if !bytes.Equal(message.Payload.Bytes(), publicKey) {
		t.Fatalf("Payload = %x, want %x", message.Payload.Bytes(), publicKey)
	}
}
