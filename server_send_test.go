package coalago

import (
	"bytes"
	"testing"
	"time"
)

// Server.Send как исходящий клиент с постоянного порта: раньше block2-ответ на таком
// пути не собирался (Send возвращал пустую ACK-преамбулу), а потерянная на той стороне
// сессия обнаруживалась только по таймауту.

func startTestServer(t *testing.T, s *Server) string {
	t.Helper()
	addr, err := s.ListenAsync("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenAsync: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return addr.String()
}

func testPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func newGetMessage(path, scheme string) *CoAPMessage {
	msg := NewCoAPMessage(CON, GET)
	if scheme == "coaps" {
		msg.SetSchemeCOAPS()
	} else {
		msg.SetSchemeCOAP()
	}
	msg.SetURIPath(path)
	msg.Timeout = 2 * time.Second
	return msg
}

func TestListenAsync_ReturnsBoundAddr(t *testing.T) {
	s := NewServer()
	addr := startTestServer(t, s)
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("expected a concrete bound address, got %q", addr)
	}
}

func TestServerSend_SmallResponse(t *testing.T) {
	peer := NewServer()
	peer.GET("/small", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewStringPayload("ok"), CoapCodeContent)
	})
	peerAddr := startTestServer(t, peer)

	me := NewServer()
	startTestServer(t, me)

	for _, scheme := range []string{"coaps", "coap"} {
		rsp, err := me.Send(newGetMessage("/small", scheme), peerAddr)
		if err != nil {
			t.Fatalf("%s: Send: %v", scheme, err)
		}
		if rsp.Code != CoapCodeContent || rsp.Payload.String() != "ok" {
			t.Fatalf("%s: unexpected response code=%v payload=%q", scheme, rsp.Code, rsp.Payload.String())
		}
	}
}

func TestServerSend_Block2Response(t *testing.T) {
	want := testPayload(5*MAX_PAYLOAD_SIZE + 17)
	peer := NewServer()
	peer.GET("/big", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewBytesPayload(want), CoapCodeContent)
	})
	peerAddr := startTestServer(t, peer)

	me := NewServer()
	startTestServer(t, me)

	for _, scheme := range []string{"coaps", "coap"} {
		rsp, err := me.Send(newGetMessage("/big", scheme), peerAddr)
		if err != nil {
			t.Fatalf("%s: Send: %v", scheme, err)
		}
		if rsp.Code != CoapCodeContent {
			t.Fatalf("%s: code %v, want %v", scheme, rsp.Code, CoapCodeContent)
		}
		if got := rsp.Payload.Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("%s: payload mismatch: got %d bytes, want %d", scheme, len(got), len(want))
		}
	}
}

// Сессия потеряна на принимающей стороне (рестарт, TTL), а у нас ещё в кэше: ответ 4.01
// должен сразу привести к новому handshake, без ожидания таймаута отправки.
func TestServerSend_RecoversLostSessionFast(t *testing.T) {
	peer := NewServer()
	peer.GET("/small", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewStringPayload("ok"), CoapCodeContent)
	})
	peerAddr := startTestServer(t, peer)

	me := NewServer()
	meAddr := startTestServer(t, me)

	send := func() {
		t.Helper()
		rsp, err := me.Send(newGetMessage("/small", "coaps"), peerAddr)
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if rsp.Payload.String() != "ok" {
			t.Fatalf("unexpected payload %q", rsp.Payload.String())
		}
	}

	send()

	peer.sessions.Delete(peer.sr.conn.LocalAddr().String(), meAddr, "")

	started := time.Now()
	send()
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("recovery took %v: expected an immediate re-handshake, not a send timeout", elapsed)
	}
}
