package session

import (
	"bytes"
	"testing"
)

// Demonstrates finding S2: the AES-GCM nonce is derived from the 16-bit CoAP
// MessageID (makeNonce writes only a uint16 counter into a 12-byte nonce, the
// upper 6 bytes stay zero). Two messages that share a MessageID (a retransmit,
// or any pair after the counter wraps at 65536) reuse the (key, nonce) pair.
// GCM is CTR-mode, so identical keystream cancels under XOR and an eavesdropper
// recovers plaintext1 XOR plaintext2 WITHOUT the key.
func TestNonceReuse_RecoversPlaintextXorWithoutKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 16)
	iv := []byte{1, 2, 3, 4}

	aead, err := NewAEAD(key, key, iv, iv)
	if err != nil {
		t.Fatal(err)
	}

	p1 := []byte("secret-password!") // 16 bytes
	p2 := []byte("another-secret!!") // 16 bytes
	var msgID uint16 = 4242          // same CoAP MessageID => same nonce

	c1 := aead.Seal(p1, msgID, nil)
	c2 := aead.Seal(p2, msgID, nil)

	for i := range p1 {
		if c1[i]^c2[i] != p1[i]^p2[i] {
			t.Fatalf("byte %d: keystream did not cancel — nonce-reuse model wrong", i)
		}
	}
	t.Logf("NONCE REUSE CONFIRMED: same MessageID=%d -> c1^c2 == p1^p2 for all %d bytes; "+
		"attacker recovers plaintext XOR with no key.", msgID, len(p1))

	// The nonce only varies in its low 16 bits: bytes [6:12) are always zero.
	n := makeNonce(iv, 0xFFFF)
	for i := 6; i < 12; i++ {
		if n[i] != 0 {
			t.Fatalf("nonce byte %d is non-zero; nonce space is wider than assumed", i)
		}
	}
}
