package coalago_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"

	. "github.com/onsi/ginkgo/extensions/table"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	. "github.com/coalalib/coalago"
)

var _ = Describe("Message", func() {
	Describe("Serialize message", func() {
		var (
			message  *CoAPMessage
			datagram []byte
			err      error
		)

		BeforeEach(func() {
			message = NewCoAPMessage(CON, GET)
			datagram, err = Serialize(message)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			message = nil
		})

		Context("With correct Message ID", func() {
			It("Should correct serialize message id", func() {
				uint16DatagramSlice := binary.BigEndian.Uint16(datagram[2:4])
				Expect(uint16DatagramSlice).Should(Equal(message.MessageID))
			})
		})

		Context("With correct Version", func() {
			It("Should correct serialize version", func() {
				Expect(datagram[0] >> 6).Should(Equal(uint8(1)))
			})
		})

		Context("With Type", func() {
			DescribeTable("Check each type",
				func(expectedType CoapType) {
					message.Type = expectedType
					datagram, err = Serialize(message)
					Expect(err).NotTo(HaveOccurred())
					Expect(datagram[0] >> 4 & 3).Should(Equal(uint8(expectedType)))
				},
				Entry("CON", CON),
				Entry("NON", NON),
				Entry("ACK", ACK),
				Entry("RST", RST),
			)
		})

		Context("With Token Length", func() {
			DescribeTable("Check each any length",
				func(tokenLength int, isOk bool) {
					token := make([]byte, tokenLength)
					rand.Read(token)
					message.Token = token

					datagram, err = Serialize(message)
					Expect(err == nil).Should(Equal(isOk))
				},
				Entry("Token length is zero", 0, true),
				Entry("Token length is valid", 5, true),
				Entry("Token length is maximum", 8, true),
				// Entry("Token length is out of range", 9, false),
			)
		})

		Context("With correct codes", func() {
			DescribeTable("Check each code",
				func(expectedCode CoapCode) {
					message.Code = expectedCode
					datagram, err = Serialize(message)
					Expect(err).NotTo(HaveOccurred())
					Expect(datagram[1]).Should(Equal(uint8(expectedCode)))
				},

				//methods
				Entry("GET", GET),
				Entry("POST", POST),
				Entry("PUT", PUT),
				Entry("DELETE", DELETE),

				// Response
				Entry("CoapCodeEmpty", CoapCodeEmpty),
				Entry("CoapCodeCreated", CoapCodeCreated),
				Entry("CoapCodeDeleted", CoapCodeDeleted),
				Entry("CoapCodeValid", CoapCodeValid),
				Entry("CoapCodeChanged", CoapCodeChanged),
				Entry("CoapCodeContent", CoapCodeContent),
				Entry("CoapCodeContinue", CoapCodeContinue),

				// Errors
				Entry("CoapCodeBadRequest", CoapCodeBadRequest),
				Entry("CoapCodeUnauthorized", CoapCodeUnauthorized),
				Entry("CoapCodeBadOption", CoapCodeBadOption),
				Entry("CoapCodeForbidden", CoapCodeForbidden),
				Entry("CoapCodeNotFound", CoapCodeNotFound),
				Entry("CoapCodeMethodNotAllowed", CoapCodeMethodNotAllowed),
				Entry("CoapCodeNotAcceptable", CoapCodeNotAcceptable),
				Entry("CoapCodeRequestEntityIncomplete", CoapCodeRequestEntityIncomplete),
				Entry("CoapCodeConflict", CoapCodeConflict),
				Entry("CoapCodePreconditionFailed", CoapCodePreconditionFailed),
				Entry("CoapCodeRequestEntityTooLarge", CoapCodeRequestEntityTooLarge),
				Entry("CoapCodeUnsupportedContentFormat", CoapCodeUnsupportedContentFormat),
				Entry("CoapCodeInternalServerError", CoapCodeInternalServerError),
				Entry("CoapCodeNotImplemented", CoapCodeNotImplemented),
				Entry("CoapCodeBadGateway", CoapCodeBadGateway),
				Entry("CoapCodeServiceUnavailable", CoapCodeServiceUnavailable),
				Entry("CoapCodeGatewayTimeout", CoapCodeGatewayTimeout),
				Entry("CoapCodeProxyingNotSupported", CoapCodeProxyingNotSupported),
			)
		})

		Context("With correct Token", func() {
			DescribeTable("Check each token by length",
				func(tokenLength int) {
					token := make([]byte, tokenLength)
					rand.Read(token)
					message.Token = token

					datagram, err = Serialize(message)
					Expect(err).NotTo(HaveOccurred())
					Expect(bytes.Equal(datagram[4:4+tokenLength], message.Token)).Should(BeTrue())
				},
				Entry("Token length is minimum", 1),
				Entry("Token length is valid", 5),
				Entry("Token length is maximum", 8),
				// Entry("Token length is out of range", 9, false),
			)
		})
	})

	Describe("Serialize and deserialize payloads with special characters", func() {
		roundTrip := func(payload CoAPMessagePayload) *CoAPMessage {
			message := NewCoAPMessage(CON, POST)
			message.MessageID = 0x1234
			message.Token = []byte{0x00, 0x01, 0xfe, 0xff}
			message.Payload = payload
			message.SetMediaType(MediaTypeApplicationOctetStream)

			datagram, err := Serialize(message)
			Expect(err).NotTo(HaveOccurred())

			result, err := Deserialize(datagram)
			Expect(err).NotTo(HaveOccurred())
			return result
		}

		DescribeTable("preserves string payload bytes exactly",
			func(body string) {
				result := roundTrip(NewStringPayload(body))

				Expect(result.Payload.Bytes()).To(Equal([]byte(body)))
				Expect(result.Payload.String()).To(Equal(body))
			},
			Entry("quotes, slashes, whitespace and URL delimiters", "line1\nline2\r\n\t\"quoted\" 'single' \\\\ / ?&=%#[]{}()<>"),
			Entry("unicode escape sequences", "\u041f\u0440\u0438\u0432\u0435\u0442, \u4e16\u754c, \U0001f680"),
			Entry("NUL byte and CoAP payload marker byte", "prefix\x00middle\xffsuffix"),
		)

		It("preserves arbitrary binary payload bytes, including embedded payload markers", func() {
			body := make([]byte, 256)
			for i := range body {
				body[i] = byte(i)
			}

			result := roundTrip(NewBytesPayload(body))

			Expect(result.Payload.Bytes()).To(Equal(body))
		})

		It("preserves every possible single-byte payload value", func() {
			for i := 0; i <= 0xff; i++ {
				body := []byte{'p', 'r', 'e', byte(i), 'p', 'o', 's', 't'}

				result := roundTrip(NewBytesPayload(body))

				Expect(result.Payload.Bytes()).To(Equal(body), fmt.Sprintf("byte 0x%02x", i))
			}
		})

		It("preserves all ASCII special and punctuation characters in string payloads", func() {
			body := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

			result := roundTrip(NewStringPayload(body))

			Expect(result.Payload.String()).To(Equal(body))
			Expect(result.Payload.Bytes()).To(Equal([]byte(body)))
		})

		It("preserves ASCII control characters in string payloads", func() {
			body := string([]byte{
				0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
				0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
				0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
				0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
				0x7f,
			})

			result := roundTrip(NewStringPayload(body))

			Expect(result.Payload.String()).To(Equal(body))
			Expect(result.Payload.Bytes()).To(Equal([]byte(body)))
		})

		It("preserves common Unicode edge-case characters in string payloads", func() {
			body := "\u041f\u0440\u0438\u0432\u0435\u0442 " +
				"\u4e16\u754c " +
				"\u0301\u0327\u20dd " +
				"\u200b\u200c\u200d\ufeff " +
				"\u00a0\u2028\u2029 " +
				"\U0001f600\U0001f680\U0001f469\u200d\U0001f4bb"

			result := roundTrip(NewStringPayload(body))

			Expect(result.Payload.String()).To(Equal(body))
			Expect(result.Payload.Bytes()).To(Equal([]byte(body)))
		})

		It("serializes JSON payloads with special characters into valid JSON", func() {
			source := map[string]interface{}{
				"text":    "line1\nline2\r\n\t\"quoted\" \\\\ / <tag>&value",
				"unicode": "\u041f\u0440\u0438\u0432\u0435\u0442, \u4e16\u754c, \U0001f680",
			}

			result := roundTrip(NewJSONPayload(source))

			var decoded map[string]string
			Expect(json.Unmarshal(result.Payload.Bytes(), &decoded)).To(Succeed())
			Expect(decoded["text"]).To(Equal(source["text"]))
			Expect(decoded["unicode"]).To(Equal(source["unicode"]))
		})

		It("serializes JSON payloads containing all ASCII controls and punctuation", func() {
			source := map[string]string{
				"controls": string([]byte{
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
					0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
					0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
					0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
					0x7f,
				}),
				"punctuation": "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
			}

			result := roundTrip(NewJSONPayload(source))

			var decoded map[string]string
			Expect(json.Unmarshal(result.Payload.Bytes(), &decoded)).To(Succeed())
			Expect(decoded).To(Equal(source))
		})
	})
})
