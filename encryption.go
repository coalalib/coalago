package coalago

import (
	"net/url"

	"github.com/coalalib/coalago/session"
)

func encrypt(message *CoAPMessage, address string, aead session.AEAD) error {
	if message.Payload != nil && message.Payload.Length() != 0 {
		var associatedData []byte
		message.Payload = NewBytesPayload(aead.Seal(message.Payload.Bytes(), message.MessageID, associatedData))
	}

	err := encryptionOptions(message, address, aead)
	if err != nil {
		return err
	}

	return nil
}

func decrypt(message *CoAPMessage, aead session.AEAD) error {
	if message.Payload != nil && message.Payload.Length() != 0 {
		var associatedData []byte
		newPayload, err := aead.Open(message.Payload.Bytes(), message.MessageID, associatedData)
		if err != nil {
			return err
		}
		message.Payload = NewBytesPayload(newPayload)
	}

	return decryptionOptions(message, aead)
}

func encryptionOptions(message *CoAPMessage, address string, aead session.AEAD) error {
	var associatedData []byte

	coapsURI := aead.Seal([]byte(message.GetURI(address)), message.MessageID, associatedData)
	message.RemoveOptions(OptionURIPath)
	message.RemoveOptions(OptionURIQuery)
	message.AddOption(OptionСoapsUri, string(coapsURI))

	return nil
}

func decryptionOptions(message *CoAPMessage, aead session.AEAD) error {
	coapsURIOption := message.GetOption(OptionСoapsUri)
	if coapsURIOption == nil {
		return nil
	}

	var associatedData []byte
	coapsURI, err := aead.Open([]byte(coapsURIOption.StringValue()), message.MessageID, associatedData)
	if err != nil {
		return err
	}

	parsedURL, err := url.Parse(string(coapsURI))
	if err != nil {
		return err
	}
	queries, err := url.ParseQuery(parsedURL.RawQuery)
	if err != nil {
		return err
	}

	message.SetURIPath(parsedURL.Path)

	for k, v := range queries {
		message.SetURIQuery(k, v[0])
	}

	message.RemoveOptions(OptionСoapsUri)
	return nil
}
