package coalago

func requestOnReceive(resource *CoAPResource, sr *transport, message *CoAPMessage) bool {
	if message.Code < 0 || message.Code > 4 {
		return true
	}

	if isPing(message) {
		return returnPing(sr, message)
	}

	if resource == nil {
		if message.Type == CON {
			return noResource(sr, message)
		}
		return false
	}

	if resource.Method != message.GetMethod() {
		return methodNotAllowed(sr, message)
	}

	if handlerResult := resource.Handler(message); handlerResult != nil {
		if message.Type == NON {
			return false
		}
		return returnResultFromResource(sr, message, handlerResult)
	}

	if message.Type == CON {
		return false
	}
	return false
}

func isPing(message *CoAPMessage) bool {
	return message.Type == CON && message.Code == CoapCodeEmpty
}

func returnPing(sr *transport, message *CoAPMessage) bool {
	resp := NewCoAPMessage(RST, CoapCodeEmpty)
	resp.MessageID = message.MessageID
	copy(resp.Token, message.Token)
	_, err := sr.SendTo(resp, message.Sender)

	return err != nil
}

func methodNotAllowed(sr *transport, message *CoAPMessage) bool {
	responseMessage := NewCoAPMessageId(ACK, CoapCodeMethodNotAllowed, message.MessageID)
	responseMessage.Payload = NewStringPayload("Method is not allowed for requested resource")
	if message.Token != nil && len(message.Token) > 0 {
		responseMessage.Token = message.Token
	}
	responseMessage.CloneOptions(message, OptionBlock1, OptionBlock2, OptionProxySecurityID)
	sr.SendTo(responseMessage, message.Sender)
	return false
}

func returnResultFromResource(sr *transport, message *CoAPMessage, handlerResult *CoAPResourceHandlerResult) bool {
	// @TODO: Validate Response code! handlerResult.Code

	// Create ACK response with the same ID and given reponse Code
	responseMessage := NewCoAPMessageId(ACK, handlerResult.Code, message.MessageID)
	responseMessage.Payload = handlerResult.Payload

	// Replicate Token of the original message if any
	responseMessage.Token = message.Token

	// Setup additional Content Format description if necessary
	if handlerResult.MediaType >= 0 {
		responseMessage.AddOption(OptionContentFormat, handlerResult.MediaType)
	}

	// validate Observe option (add Option in Response upon registration!)
	if option := message.GetOption(OptionObserve); option != nil && option.IntValue() == 0 {
		responseMessage.AddOption(OptionObserve, 1)
	}

	// Validate message scheme
	if message.GetScheme() == COAPS_SCHEME {
		responseMessage.SetSchemeCOAPS()
	}
	responseMessage.CloneOptions(message, OptionBlock1, OptionBlock2, OptionSelectiveRepeatWindowSize, OptionProxySecurityID)

	_, err := sr.SendTo(responseMessage, message.Sender)
	return err != nil
}

func noResource(sr *transport, message *CoAPMessage) bool {
	responseMessage := NewCoAPMessageId(ACK, CoapCodeNotFound, message.MessageID)
	responseMessage.Payload = NewStringPayload("Requested resource " + message.GetURIPath() + " does not exist")
	if message.Token != nil && len(message.Token) > 0 {
		responseMessage.Token = message.Token
	}
	responseMessage.CloneOptions(message, OptionBlock1, OptionBlock2, OptionProxySecurityID)
	responseMessage.Recipient = message.Sender

	_, err := sr.SendTo(responseMessage, message.Sender)
	return err != nil
}
