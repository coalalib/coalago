package coalago_test

import (
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
})
