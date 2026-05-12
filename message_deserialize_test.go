package coalago_test

import (
	"errors"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	. "github.com/coalalib/coalago"
)

var _ = Describe("Deserialize malformed messages", func() {
	It("returns an error for datagrams shorter than the CoAP header", func() {
		_, err := Deserialize([]byte{0x40, byte(GET), 0x00})

		Expect(errors.Is(err, ErrPacketLengthLessThan4)).To(BeTrue())
	})

	It("returns an error for an invalid CoAP version", func() {
		_, err := Deserialize([]byte{0x00, byte(GET), 0x00, 0x01})

		Expect(errors.Is(err, ErrInvalidCoapVersion)).To(BeTrue())
	})

	It("returns an error when option delta uses reserved value 15", func() {
		datagram := []byte{0x40, byte(GET), 0x00, 0x01, 0xf0}

		_, err := Deserialize(datagram)

		Expect(errors.Is(err, ErrOptionDeltaUsesValue15)).To(BeTrue())
	})

	It("returns an error when option length uses reserved value 15", func() {
		datagram := []byte{0x40, byte(GET), 0x00, 0x01, 0x0f}

		_, err := Deserialize(datagram)

		Expect(errors.Is(err, ErrOptionLengthUsesValue15)).To(BeTrue())
	})

	It("turns panics from truncated extended options into ErrNilMessage", func() {
		datagram := []byte{0x40, byte(GET), 0x00, 0x01, 0xd0}

		_, err := Deserialize(datagram)

		Expect(errors.Is(err, ErrNilMessage)).To(BeTrue())
	})
})
