package coalago_test

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	. "github.com/coalalib/coalago"
)

var _ = Describe("Options", func() {
	It("replaces non-repeatable options", func() {
		message := NewCoAPMessage(CON, GET)

		message.AddOption(OptionContentFormat, MediaTypeTextPlain)
		message.AddOption(OptionContentFormat, MediaTypeApplicationJSON)

		options := message.GetOptions(OptionContentFormat)
		Expect(options).To(HaveLen(1))
		Expect(options[0].IntValue()).To(Equal(int(MediaTypeApplicationJSON)))
	})

	It("keeps repeatable URI query options", func() {
		message := NewCoAPMessage(CON, GET)

		message.SetURIQuery("plain", "value")
		message.SetURIQuery("special key", "value/with spaces&symbols=%")

		Expect(message.GetURIQueryArray()).To(Equal([]string{
			"plain=value",
			"special key=value/with spaces&symbols=%",
		}))
		Expect(message.GetURIQuery("special key")).To(Equal("value/with spaces&symbols=%"))
	})

	It("escapes URI query values while building a URI", func() {
		message := NewCoAPMessage(CON, GET)
		message.SetURIPath("/sensor/data")
		message.SetURIQuery("special key", "value/with spaces&symbols=%")

		Expect(message.GetURI("127.0.0.1:5683")).To(Equal("coap://127.0.0.1:5683/sensor/data?special+key=value%2Fwith+spaces%26symbols%3D%25"))
	})

	It("preserves string option values with special characters after serialization", func() {
		message := NewCoAPMessage(CON, GET)
		message.Token = []byte{0x01, 0x02}
		message.AddOption(OptionURIPath, "devices")
		message.AddOption(OptionURIQuery, "name=alpha beta/42&raw=%")
		message.AddOption(OptionProxyURI, "coap://host/path?x=1&y=two words")

		datagram, err := Serialize(message)
		Expect(err).NotTo(HaveOccurred())

		result, err := Deserialize(datagram)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.GetOptionsAsString(OptionURIPath)).To(Equal([]string{"devices"}))
		Expect(result.GetOptionsAsString(OptionURIQuery)).To(Equal([]string{"name=alpha beta/42&raw=%"}))
		Expect(result.GetOptionProxyURIasString()).To(Equal("coap://host/path?x=1&y=two words"))
	})

	It("preserves every printable ASCII special character in URI query options", func() {
		specials := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

		for _, r := range specials {
			value := fmt.Sprintf("key=%c", r)
			message := NewCoAPMessage(CON, GET)
			message.Token = []byte{0x01, 0x02}
			message.AddOption(OptionURIQuery, value)

			datagram, err := Serialize(message)
			Expect(err).NotTo(HaveOccurred())

			result, err := Deserialize(datagram)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.GetOptionsAsString(OptionURIQuery)).To(Equal([]string{value}), fmt.Sprintf("rune %q", r))
		}
	})

	It("preserves control characters in option values", func() {
		value := string([]byte{
			0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
			0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
			0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
			0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
			0x7f,
		})
		message := NewCoAPMessage(CON, GET)
		message.Token = []byte{0x01, 0x02}
		message.AddOption(OptionURIQuery, value)

		datagram, err := Serialize(message)
		Expect(err).NotTo(HaveOccurred())

		result, err := Deserialize(datagram)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.GetOptionsAsString(OptionURIQuery)).To(Equal([]string{value}))
	})

	It("preserves Unicode edge-case characters in option values", func() {
		value := "key=\u041f\u0440\u0438\u0432\u0435\u0442-\u4e16\u754c-\u0301\u0327\u20dd-\u200b\u200c\u200d\ufeff-\U0001f600"
		message := NewCoAPMessage(CON, GET)
		message.Token = []byte{0x01, 0x02}
		message.AddOption(OptionURIQuery, value)

		datagram, err := Serialize(message)
		Expect(err).NotTo(HaveOccurred())

		result, err := Deserialize(datagram)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.GetOptionsAsString(OptionURIQuery)).To(Equal([]string{value}))
	})
})
