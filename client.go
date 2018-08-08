package coalago

import (
	"errors"
	"net"
	"net/url"
	"time"

	m "github.com/coalalib/coalago/message"
	cache "github.com/patrickmn/go-cache"
)

var (
	ErrUndefinedScheme = errors.New("undefined scheme")
	ErrMaxAttempts     = errors.New("max attempts")
	MAX_PAYLOAD_SIZE   = 512
)

type Response struct {
	Body []byte
	Code m.CoapCode
}

type Client struct {
	sessions *cache.Cache
}

func NewClient() *Client {
	c := new(Client)
	c.sessions = cache.New(SESSIONS_POOL_EXPIRATION, time.Second*10)
	return c
}

func (c *Client) GET(url string) (*Response, error) {
	message, err := constructMessage(m.GET, url)
	if err != nil {
		return nil, err
	}
	return clientSendCONMessage(message, message.Recipient.String())
}

func (c *Client) POST(data []byte, url string) (*Response, error) {
	message, err := constructMessage(m.POST, url)
	if err != nil {
		return nil, err
	}
	message.Payload = m.NewBytesPayload(data)
	return clientSendCONMessage(message, message.Recipient.String())
}

func (c *Client) DELETE(data []byte, url string) (*Response, error) {
	message, err := constructMessage(m.DELETE, url)
	if err != nil {
		return nil, err
	}
	return clientSendCONMessage(message, message.Recipient.String())
}

func clientSendCONMessage(message *m.CoAPMessage, addr string) (*Response, error) {
	resp, err := clientSendCON(message, addr)
	if err != nil {
		return nil, err
	}
	r := new(Response)
	r.Body = resp.Payload.Bytes()
	r.Code = resp.Code

	return r, nil
}

func clientSendCON(message *m.CoAPMessage, addr string) (resp *m.CoAPMessage, err error) {
	conn, err := globalPoolConnections.Dial(addr)
	if err != nil {
		return nil, err
	}

	defer conn.Close()

	sr := newtransport(conn)
	return sr.sendCON(message)
}

func constructMessage(code m.CoapCode, url string) (*m.CoAPMessage, error) {
	path, scheme, queries, addr, err := parseURI(url)
	if err != nil {
		return nil, err
	}

	message := m.NewCoAPMessage(m.CON, code)
	switch scheme {
	case "coap":
		message.SetSchemeCOAP()
	case "coaps":
		message.SetSchemeCOAPS()
	default:
		return nil, ErrUndefinedScheme
	}

	message.SetURIPath(path)

	for k, v := range queries {
		message.SetURIQuery(k, v[0])
	}

	message.Recipient = addr

	return message, nil
}

func parseURI(uri string) (path, scheme string, queries url.Values, addr net.Addr, err error) {
	var u *url.URL
	u, err = url.Parse(uri)
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

func isBigPayload(message *m.CoAPMessage) bool {
	return message.Payload.Length() > MAX_PAYLOAD_SIZE
}