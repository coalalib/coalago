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
	useTCP     bool
	pool       *connpool
}

func NewClient(opts ...Opt) *Client {
	options := &coalaopts{}
	for _, opt := range opts {
		opt(options)
	}

	return &Client{
		privateKey: options.privatekey,
		pool:       newConnpool(false),
	}
}

func NewTCPClient(opts ...Opt) *Client {
	options := &coalaopts{}
	for _, opt := range opts {
		opt(options)
	}
	return &Client{
		privateKey: options.privatekey,
		pool:       newConnpool(true),
	}
}

func (c *Client) Send(message *CoAPMessage, addr string, options ...*CoAPMessageOption) (*Response, error) {
	message.AddOptions(options)

	conn, err := c.pool.Dial(addr)
	if err != nil {
		return nil, err
	}

	defer conn.Close()

	sr := newtransport(conn)
	sr.privateKey = c.privateKey

	resp, err := sr.Send(message)
	if err != nil {
		return nil, err
	}
	switch message.Type {
	case NON, ACK:
		return nil, nil
	}
	r := new(Response)
	r.Body = resp.Payload.Bytes()
	r.Code = resp.Code
	r.PeerPublicKey = resp.PeerPublicKey
	return r, nil
}

func (c *Client) GET(uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(GET, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	return c.sendCONMessage(msg)
}

func (c *Client) POST(data []byte, uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(POST, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	msg.Payload = NewBytesPayload(data)
	return c.sendCONMessage(msg)
}

func (c *Client) DELETE(data []byte, uri string, opts ...*CoAPMessageOption) (*Response, error) {
	msg, err := constructMessage(DELETE, uri)
	if err != nil {
		return nil, err
	}
	msg.AddOptions(opts)
	return c.sendCONMessage(msg)
}

func (c *Client) sendCONMessage(msg *CoAPMessage) (*Response, error) {
	resp, err := c.sendCON(msg)
	if err != nil {
		return nil, err
	}
	return &Response{
		Body:          resp.Payload.Bytes(),
		Code:          resp.Code,
		PeerPublicKey: resp.PeerPublicKey,
	}, nil
}

func (c *Client) sendCON(msg *CoAPMessage) (*CoAPMessage, error) {
	conn, err := c.pool.Dial(msg.Recipient.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	sr := newtransport(conn)
	sr.privateKey = c.privateKey
	return sr.Send(msg)
}

func constructMessage(code CoapCode, uri string) (*CoAPMessage, error) {
	path, scheme, queries, addr, err := parseURI(uri)
	if err != nil {
		return nil, err
	}

	msg := NewCoAPMessage(CON, code)
	switch scheme {
	case "coap", "coap+tcp":
		msg.SetSchemeCOAP()
	case "coaps", "coaps+tcp":
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
