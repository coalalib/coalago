# Coala Go

Go implementation of Coala on top of CoAP messages. The module provides a
peer-to-peer CoAP client/server stack over UDP and TCP, plus Coala extensions
for secure sessions, proxying, and large payload transfer.

Coala Go includes:

- UDP client/server API over CoAP datagram encoding.
- TCP client/server API with length-prefixed CoAP frames.
- Resources with `GET`, `POST`, `PUT`, and `DELETE` handlers.
- Synchronous request/response client helpers.
- Confirmable message retries and timeout handling.
- Proxy options and proxy session tracking.
- Block1/Block2 and selective-repeat ARQ for large payloads.
- `coaps` handshake/encryption with X25519, HKDF-SHA256, and AES-GCM using
  Coala's 12-byte authentication tag format.
- Message serializer/deserializer and a small `coap-cli` command.

## Requirements

- Go `1.25.0` as declared in `go.mod`
- UDP/TCP networking available on the target platform

```bash
go mod download
go test ./...
```

## Installation

```bash
go get github.com/coalalib/coalago
```

Command-line client:

```bash
go install github.com/coalalib/coalago/cmd/coap-cli@latest
```

## Examples

All runnable examples live under `examples/`:

| Path | Description |
| --- | --- |
| `examples/basic` | UDP client/server large payload example. |
| `examples/tcp` | TCP client/server example. |
| `examples/tcp_nat` | TCP/UDP proxy and NAT-style device example. |

Run an example by starting its server and client in separate shells:

```bash
go run ./examples/basic server
go run ./examples/basic client

go run ./examples/tcp --mode server
go run ./examples/tcp --mode client
```

## Quick Start

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/coalalib/coalago"
)

func main() {
	server := coalago.NewServer()
	server.GET("/msg", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		return coalago.NewResponse(
			coalago.NewStringPayload("Hello from Coala Go"),
			coalago.CoapCodeContent,
		)
	})

	go func() {
		if err := server.Listen(":5683"); err != nil {
			log.Fatal(err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	client := coalago.NewClient()
	response, err := client.GET("coap://127.0.0.1:5683/msg")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(response.Body))
}
```

## CLI

`coap-cli` sends one CoAP request and prints the response:

```bash
coap-cli coap://127.0.0.1:5683/info
coap-cli -m post -d "hello" coap://127.0.0.1:5683/data
coap-cli -key mysecret "coaps://127.0.0.1:5683/info"
```

Supported flags:

| Flag | Description |
| --- | --- |
| `-m` | Method: `get`, `post`, `put`, or `delete`. |
| `-d` | Payload for `POST`/`PUT`. |
| `-key` | Private key seed for `coaps` requests. |
| `-v` | Verbose request/response output. |

## Main API

### Client

| API | Description |
| --- | --- |
| `NewClient(opts...)` | Creates a UDP client. |
| `NewTCPClient(opts...)` | Creates a TCP client. |
| `GET(uri, opts...)` | Sends a confirmable GET request. |
| `POST(data, uri, opts...)` | Sends a confirmable POST request with payload. |
| `DELETE(data, uri, opts...)` | Sends a confirmable DELETE request. |
| `Send(message, addr, opts...)` | Sends a custom `CoAPMessage` to `host:port`. |

Client options:

| API | Description |
| --- | --- |
| `WithPrivateKey(seed)` | Uses a deterministic X25519 private key derived from `SHA-256(seed)`. |

### Server

| API | Description |
| --- | --- |
| `NewServer(opts...)` | Creates a Coala server. |
| `Listen(addr)` | Starts a blocking UDP listener, for example `":5683"`. |
| `ListenTCP(addr)` | Starts a blocking TCP listener. |
| `Refresh()` | Recreates the listener on the saved address. |
| `GET`, `POST`, `PUT`, `DELETE` | Registers a resource handler for a method/path pair. |
| `Send(message, addr, opts...)` | Sends a message from the server socket. |
| `Serve(conn)` | Uses an externally created UDP connection. |
| `ServeMessage(message)` | Processes a message as if it was received from the network. |
| `Proxy(flag)` | Enables/disables proxy behavior for the server. |
| `SetPrivateKey`, `GetPrivateKey` | Sets or reads the server private key seed. |
| `IsUDP`, `IsTCP`, `GetConnectionType` | Reads current transport mode flags. |

Server send options:

| API | Description |
| --- | --- |
| `WithRetries(n)` | Retries server-side `Send` up to `n` additional times. |

### TCP

Use `NewTCPClient()` with `coap+tcp://` or `coaps+tcp://` URIs:

```go
server := coalago.NewServer()
server.GET("/msg", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
	return coalago.NewResponse(coalago.NewStringPayload("tcp"), coalago.CoapCodeContent)
})
go server.ListenTCP(":5683")

client := coalago.NewTCPClient()
response, err := client.GET("coap+tcp://127.0.0.1:5683/msg")
```

## Resources

| API | Description |
| --- | --- |
| `NewCoAPResource(method, path, handler)` | Creates a resource value. Most code uses server method helpers instead. |
| `CoAPResourceHandler` | Function type that receives `*CoAPMessage` and returns `*CoAPResourceHandlerResult`. |
| `NewResponse(payload, code)` | Builds a resource response. |
| `NewStringPayload`, `NewBytesPayload`, `NewJSONPayload`, `NewEmptyPayload` | Payload implementations. |

Example `POST` resource:

```go
server.POST("/config", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
	fmt.Printf("Payload: %s\n", message.Payload.String())
	fmt.Printf("mode query: %s\n", message.GetURIQuery("mode"))

	return coalago.NewResponse(
		coalago.NewStringPayload("changed"),
		coalago.CoapCodeChanged,
	)
})
```

## Messages and Methods

### CoAP Methods

| Method | Purpose |
| --- | --- |
| `GET` | Read a resource representation or state. |
| `POST` | Send a command or create/update subordinate state. |
| `PUT` | Replace or set resource state. |
| `DELETE` | Delete a resource or clear state. |

Server resource methods use `CoapMethodGet`, `CoapMethodPost`,
`CoapMethodPut`, and `CoapMethodDelete`.

### Reliability Types

| Type | Purpose |
| --- | --- |
| `CON` | Requires ACK/RST and is retransmitted until timeout. |
| `NON` | Sends without requiring ACK. |
| `ACK` | Acknowledges a confirmable message. |
| `RST` | Rejects a message or missing context. |

### `CoAPMessage`

| API | Description |
| --- | --- |
| `NewCoAPMessage(type, code)` | Creates a message with generated message ID and token. |
| `NewCoAPMessageId(type, code, id)` | Creates a message with explicit message ID. |
| `Serialize(message)` | Encodes a message into a CoAP datagram. |
| `Deserialize(data)` | Decodes a CoAP datagram. |
| `SetURIPath(path)`, `GetURIPath()` | Writes/reads path through URI-Path options. |
| `SetURIQuery(k, v)`, `GetURIQuery(k)` | Writes/reads URI-Query options. |
| `SetSchemeCOAP()`, `SetSchemeCOAPS()` | Selects plaintext or secure Coala scheme. |
| `SetStringPayload`, `Payload` | Writes/reads payload. |
| `SetProxy(scheme, addr)` | Adds Proxy-Uri and stores proxy address. |
| `SetMediaType(mediaType)` | Adds Content-Format. |
| `AddOption`, `AddOptions`, `RemoveOptions` | Mutates CoAP options. |
| `GetOption`, `GetOptions`, `GetOptionAsString` | Reads CoAP options. |
| `GetBlock1`, `GetBlock2` | Reads typed Block1/Block2 option metadata. |
| `Clone(includePayload)` | Copies a message for retransmission or layer processing. |

Custom request example:

```go
message := coalago.NewCoAPMessage(coalago.CON, coalago.PUT)
message.SetURIPath("/config")
message.SetURIQuery("mode", "fast")
message.Payload = coalago.NewJSONPayload(map[string]string{"enabled": "true"})

response, err := client.Send(message, "127.0.0.1:5683")
```

## Secure Coala (`coaps`)

Use a URI with the `coaps` scheme. The handshake starts automatically:

```go
client := coalago.NewClient(coalago.WithPrivateKey([]byte("device-key")))

response, err := client.POST(
	[]byte("encrypted payload"),
	"coaps://127.0.0.1:5683/secure",
)
```

For custom messages:

```go
message := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
message.SetSchemeCOAPS()
message.SetURIPath("/secure")
```

Internally:

- X25519 key agreement.
- HKDF-SHA256 derives two AES keys and two IVs.
- Payload and encrypted URI are carried through Coala custom options.
- AES-GCM tag is truncated to 12 bytes for Coala compatibility.
- `BreakConnectionOnPK` can reject a peer public key during handshake.

## Proxy

Set a proxy on the message before sending:

```go
message := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
message.SetURIPath("/remote/info")
message.SetProxy("coap", "192.168.1.1:5683")

response, err := client.Send(message, "10.0.0.1:5683")
```

Proxy support uses `Proxy-Uri` and Coala `proxySecurityId` when secure sessions
are proxied.

## Blockwise and Large Payloads

Payloads larger than `1024` bytes are split into Block1/Block2 segments. For
Coala peers, the stack also uses selective-repeat ARQ with option
`OptionSelectiveRepeatWindowSize` (`3001`) to send a window of blocks and
reassemble the payload on the receiver.

Default ARQ/window constants:

| Constant | Value |
| --- | --- |
| `MAX_PAYLOAD_SIZE` | `1024` |
| `DEFAULT_WINDOW_SIZE` | `300` |
| `MIN_WiNDOW_SIZE` | `50` |
| `MAX_WINDOW_SIZE` | `1500` |

## Discovery and Observe

This module defines Coala/CoAP option constants used by discovery and Observe,
including `OptionObserve` and Coala's multicast/discovery-compatible wire
options. Unlike Coala Dart and Coala Java, the current Go package does not
expose a high-level multicast discovery runner or a high-level observer
registry API.

If discovery is needed, send a raw `NON GET` message to the Coala convention
endpoint:

```go
message := coalago.NewCoAPMessage(coalago.NON, coalago.GET)
message.SetURIPath("/info")
_, _ = client.Send(message, "224.0.0.187:5683")
```

## Serializer API

| API | Description |
| --- | --- |
| `Serialize(message)` | Encodes `CoAPMessage` into a CoAP datagram. |
| `Deserialize(data)` | Decodes a datagram into `CoAPMessage`. |
| `ReadTcpFrame(reader, buffer)` | Reads one length-prefixed TCP frame. |
| `WriteTcpFrame(writer, data)` | Writes one length-prefixed TCP frame. |

## How Coala Differs from CoAP

CoAP is a standard application protocol: message format, methods, response
codes, options, UDP transport, reliability model, Observe, Blockwise, and
discovery conventions. Coala Go uses the CoAP message model and wire format for
basic UDP datagrams, but adds compatibility with the Coala ecosystem.

Key differences:

- `coaps` here is not DTLS. Secure mode is implemented at the Coala layer with
  X25519 handshake, HKDF-SHA256, AES-GCM, and custom CoAP options.
- Coala defines custom options: `OptionURIScheme` (`2111`),
  `OptionSelectiveRepeatWindowSize` (`3001`), `OptionProxySecurityID` (`3004`),
  `OptionHandshakeType` (`3999`), `OptionSessionNotFound` (`4001`),
  `OptionSessionExpired` (`4003`), Coala secure URI (`4005`), and
  `OptionChecksum` (`4006`).
- Large messages can use Coala selective-repeat ARQ on top of Block1/Block2,
  not only basic CoAP blockwise exchange.
- TCP support uses a length-prefixed CoAP frame stream.
- The API is organized around client/server helpers, resources, message
  callbacks through returned responses, session storage, proxy storage, and
  metrics counters.

In short: regular CoAP peers can understand simple UDP CoAP datagrams, but Coala
features such as secure mode, selective-repeat ARQ, proxy security IDs, and
Coala-specific options require Coala extension support on the other side.
