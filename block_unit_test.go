package coalago

import "testing"

func TestBlockRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		num        int
		size       int
		moreBlocks bool
	}{
		{name: "first 16 byte block", num: 0, size: 16, moreBlocks: true},
		{name: "middle 128 byte block", num: 7, size: 128, moreBlocks: true},
		{name: "last 1024 byte block", num: 42, size: 1024, moreBlocks: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := newBlock(tt.moreBlocks, tt.num, tt.size).ToInt()
			decoded := newBlockFromInt(encoded)

			if decoded.BlockNumber != tt.num {
				t.Fatalf("BlockNumber = %d, want %d", decoded.BlockNumber, tt.num)
			}
			if decoded.BlockSize != tt.size {
				t.Fatalf("BlockSize = %d, want %d", decoded.BlockSize, tt.size)
			}
			if decoded.MoreBlocks != tt.moreBlocks {
				t.Fatalf("MoreBlocks = %t, want %t", decoded.MoreBlocks, tt.moreBlocks)
			}
		})
	}
}

func TestBlockToIntRejectsInvalidSizes(t *testing.T) {
	tests := []int{-1, 0, 2048}

	for _, size := range tests {
		t.Run("size", func(t *testing.T) {
			if got := newBlock(false, 0, size).ToInt(); got != 0 {
				t.Fatalf("ToInt() = %d, want 0 for invalid size %d", got, size)
			}
		})
	}
}

func TestConstructNextBlockSlicesPayloadAndSetsBlockOption(t *testing.T) {
	orig := NewCoAPMessage(CON, POST)
	orig.Token = []byte{0x01, 0x02, 0x03}
	orig.SetURIPath("/device/data")
	orig.SetURIQuery("kind", "temperature")
	orig.Payload = NewBytesPayload([]byte("abcdefghijklmnopqrstuvwxyz"))

	state := &stateSend{
		lenght:       orig.Payload.Length(),
		blockSize:    16,
		windowsize:   3,
		origMessage:  orig,
		payload:      orig.Payload.Bytes(),
		nextNumBlock: 0,
	}

	first, done := constructNextBlock(OptionBlock1, state)
	if done {
		t.Fatal("first block unexpectedly marked as final")
	}
	if got := string(first.Payload.Bytes()); got != "abcdefghijklmnop" {
		t.Fatalf("first payload = %q, want %q", got, "abcdefghijklmnop")
	}
	if got := first.GetBlock1(); got == nil || got.BlockNumber != 0 || !got.MoreBlocks || got.BlockSize != 16 {
		t.Fatalf("first block metadata = %#v, want number 0, more=true, size=16", got)
	}

	second, done := constructNextBlock(OptionBlock1, state)
	if !done {
		t.Fatal("second block was not marked as final")
	}
	if got := string(second.Payload.Bytes()); got != "qrstuvwxyz" {
		t.Fatalf("second payload = %q, want %q", got, "qrstuvwxyz")
	}
	if got := second.GetBlock1(); got == nil || got.BlockNumber != 1 || got.MoreBlocks || got.BlockSize != 16 {
		t.Fatalf("second block metadata = %#v, want number 1, more=false, size=16", got)
	}
}
