package coalago

import (
	"net"
	"net/url"
)

// Response представляет ответ на CoAP-запрос
type Response struct {
	Body          []byte
	Code          CoapCode
	PeerPublicKey []byte
}

// Client для отправки CoAP-запросов
type Client struct {
	privateKey []byte
}

func NewClient() *Client {
	return &Client{}
}

func NewClientWithPrivateKey(pk []byte) *Client {
	return &Client{privateKey: pk}
}

func (c *Client) GET(uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(GET, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	return clientSendCONMessage(msg, c.privateKey, msg.Recipient.String())
}

func (c *Client) POST(data []byte, uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(POST, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	msg.Payload = NewBytesPayload(data)
	return clientSendCONMessage(msg, c.privateKey, msg.Recipient.String())
}

func (c *Client) DELETE(data []byte, uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(DELETE, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	return clientSendCONMessage(msg, c.privateKey, msg.Recipient.String())
}

func clientSendCONMessage(msg *CoAPMessage, pk []byte, addr string) (*Response, error) {
	resp, err := clientSendCON(msg, pk, addr)
	if err != nil {
		return nil, err
	}
	return &Response{
		Body:          resp.Payload.Bytes(),
		Code:          resp.Code,
		PeerPublicKey: resp.PeerPublicKey,
	}, nil
}

func clientSendCON(msg *CoAPMessage, pk []byte, addr string) (*CoAPMessage, error) {
	conn, err := globalPoolConnections.Dial(addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	sr := newtransport(conn)
	sr.privateKey = pk
	return sr.Send(msg)
}

func constructMessage(code CoapCode, uri string) (*CoAPMessage, error) {
	path, scheme, queries, addr, err := parseURI(uri)
	if err != nil {
		return nil, err
	}

	msg := NewCoAPMessage(CON, code)
	switch scheme {
	case "coap":
		msg.SetSchemeCOAP()
	case "coaps":
		msg.SetSchemeCOAPS()
	default:
		return nil, ErrUndefinedScheme
	}

	msg.SetURIPath(path)
	for k, v := range queries {
		if len(v) > 0 {
			msg.SetURIQuery(k, v[0])
		}
	}
	msg.Recipient = addr

	return msg, nil
}

func parseURI(uri string) (path, scheme string, queries url.Values, addr net.Addr, err error) {
	u, err := url.Parse(uri)
	if err != nil {
		return
	}

	path = u.Path
	scheme = u.Scheme
	queries, err = url.ParseQuery(u.RawQuery)
	if err != nil {
		return
	}

	addr, _ = net.ResolveUDPAddr("udp", u.Host)
	return
}
