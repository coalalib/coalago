package coalago

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestDeserializeAcceptsValidChecksumOption(t *testing.T) {
	msg := NewCoAPMessageId(CON, POST, 0x1234)
	msg.Token = []byte{0x01, 0x02, 0x03}
	msg.AddOption(OptionURIPath, "checksum")
	msg.Payload = NewStringPayload("checksum payload")

	checksum, err := calculateChecksum(msg)
	if err != nil {
		t.Fatalf("calculate checksum: %v", err)
	}
	msg.SetChecksum(checksum)

	datagram, err := Serialize(msg)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	decoded, err := Deserialize(datagram)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if got := decoded.GetChecksum(); got != checksum {
		t.Fatalf("checksum = %q, want %q", got, checksum)
	}
}

func TestDeserializeRejectsChecksumMismatch(t *testing.T) {
	msg := NewCoAPMessageId(CON, POST, 0x1234)
	msg.Token = []byte{0x01, 0x02, 0x03}
	msg.AddOption(OptionURIPath, "checksum")
	msg.Payload = NewStringPayload("checksum payload")

	checksum, err := calculateChecksum(msg)
	if err != nil {
		t.Fatalf("calculate checksum: %v", err)
	}
	msg.SetChecksum(checksum)

	datagram, err := Serialize(msg)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	datagram[len(datagram)-1] ^= 0xff

	_, err = Deserialize(datagram)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("deserialize error = %v, want %v", err, ErrChecksumMismatch)
	}
}

func TestSerializeDoesNotAddChecksumOptionAutomatically(t *testing.T) {
	msg := NewCoAPMessageId(CON, POST, 0x1234)
	msg.Payload = NewStringPayload("checksum payload")

	datagram, err := Serialize(msg)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	decoded, err := Deserialize(datagram)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if decoded.GetOption(OptionChecksum) != nil {
		t.Fatal("checksum option was added automatically")
	}
}

func TestPreparationSendingMessageAddsChecksumWhenFlagEnabled(t *testing.T) {
	msg := NewCoAPMessageId(CON, POST, 0x1234)
	msg.Token = []byte{0x01, 0x02, 0x03}
	msg.Payload = NewStringPayload("checksum payload")
	msg.SetAddChecksumOnSend(true)

	tr := newtransport(checksumTransport{
		local:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5683},
		remote: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5684},
	})

	datagram, err := preparationSendingMessage(tr, msg, "127.0.0.1:5684")
	if err != nil {
		t.Fatalf("prepare sending message: %v", err)
	}

	decoded, err := Deserialize(datagram)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if decoded.GetChecksum() == "" {
		t.Fatal("checksum option was not added")
	}
}

type checksumTransport struct {
	local  net.Addr
	remote net.Addr
}

func (t checksumTransport) Close() error                             { return nil }
func (t checksumTransport) Listen([]byte) (int, net.Addr, error)     { return 0, nil, nil }
func (t checksumTransport) Read([]byte) (int, error)                 { return 0, nil }
func (t checksumTransport) Write([]byte) (int, error)                { return 0, nil }
func (t checksumTransport) WriteTo([]byte, string) (int, error)      { return 0, nil }
func (t checksumTransport) RemoteAddr() net.Addr                     { return t.remote }
func (t checksumTransport) LocalAddr() net.Addr                      { return t.local }
func (t checksumTransport) SetReadDeadline()                         {}
func (t checksumTransport) SetUDPRecvBuf(size int) int               { return size }
func (t checksumTransport) SetReadDeadlineSec(timeout time.Duration) {}
