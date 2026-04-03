// coap-cli is a command-line tool for sending CoAP requests.
//
// Install:
//
//	go install github.com/coalalib/coalago/cmd/coap-cli@latest
//
// Usage:
//
//	coap-cli [flags] <URI>
//
// Flags:
//
//	-m string    CoAP method: get, post, put, delete (default "get")
//	-key string  Private key for COAPS (encrypted) requests
//	-d string    Payload data for POST/PUT
//	-v           Verbose output
//
// URI format:
//
//	coap://host:port/path?query     — plaintext UDP
//	coaps://host:port/path?query    — encrypted UDP (requires -key)
//
// IPv6 addresses must be wrapped in brackets:
//
//	coap://[2a03:b0c0:3:f0::1]:5683/path
//
// Examples:
//
//	coap-cli coap://127.0.0.1:5683/info
//	coap-cli -v coap://[::1]:5683/status
//	coap-cli -m post -d "hello" coap://127.0.0.1:5683/data
//	coap-cli -key mysecretkey "coaps://127.0.0.1:5683/info"
//	coap-cli -v -key mysecretkey "coaps://[2a03:b0c0:3:f0::1]:5683/get?cid=UUID&peer_cid=data"
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/coalalib/coalago"
)

const usage = `coap-cli — CoAP command-line client (coalago)

Usage:
  coap-cli [flags] <URI>

Flags:
  -m string    CoAP method: get, post, put, delete (default "get")
  -key string  Private key for COAPS (encrypted) requests
  -d string    Payload data for POST/PUT
  -v           Verbose output

URI format:
  coap://host:port/path?query     plaintext UDP
  coaps://host:port/path?query    encrypted UDP (requires -key)

  IPv6: coap://[2a03:b0c0:3:f0::1]:5683/path

Examples:
  coap-cli coap://127.0.0.1:5683/info
  coap-cli -v coap://[::1]:5683/status
  coap-cli -m post -d "hello" coap://127.0.0.1:5683/data
  coap-cli -key mysecret "coaps://127.0.0.1:5683/info"
  coap-cli -v -key mysecret "coaps://[2a03:b0c0:3:f0::1]:5683/get?cid=UUID1&peer_cid=UUID2"

Install:
  go install github.com/coalalib/coalago/cmd/coap-cli@latest
`

func main() {
	method := flag.String("m", "get", "CoAP method: get, post, put, delete")
	key := flag.String("key", "", "Private key for COAPS encryption")
	data := flag.String("d", "", "Payload data for POST/PUT")
	verbose := flag.Bool("v", false, "Verbose output")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	uri := flag.Arg(0)

	if *verbose {
		fmt.Fprintf(os.Stderr, "[coap-cli] URI:    %s\n", uri)
		fmt.Fprintf(os.Stderr, "[coap-cli] Method: %s\n", strings.ToUpper(*method))
		if *key != "" {
			fmt.Fprintf(os.Stderr, "[coap-cli] Key:    set (%d bytes)\n", len(*key))
			fmt.Fprintf(os.Stderr, "[coap-cli] Scheme: coaps (encrypted)\n")
		} else {
			fmt.Fprintf(os.Stderr, "[coap-cli] Scheme: coap (plaintext)\n")
		}
	}

	var opts []coalago.Opt
	if *key != "" {
		opts = append(opts, coalago.WithPrivateKey([]byte(*key)))
	}

	client := coalago.NewClient(opts...)

	var resp *coalago.Response
	var err error

	start := time.Now()

	if *verbose {
		fmt.Fprintf(os.Stderr, "[coap-cli] Sending request...\n")
	}

	switch strings.ToLower(*method) {
	case "get":
		resp, err = client.GET(uri)
	case "post":
		resp, err = client.POST([]byte(*data), uri)
	case "put":
		resp, err = client.POST([]byte(*data), uri) // coalago uses POST for PUT
	case "delete":
		resp, err = client.DELETE([]byte(*data), uri)
	default:
		fmt.Fprintf(os.Stderr, "Unknown method: %s\n", *method)
		os.Exit(1)
	}

	elapsed := time.Since(start)

	if err != nil {
		if *verbose {
			fmt.Fprintf(os.Stderr, "[coap-cli] Failed after %v\n", elapsed)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[coap-cli] Response in %v\n", elapsed)
		fmt.Fprintf(os.Stderr, "[coap-cli] Code:   %d\n", resp.Code)
		fmt.Fprintf(os.Stderr, "[coap-cli] Body:   %d bytes\n", len(resp.Body))
		if len(resp.PeerPublicKey) > 0 {
			fmt.Fprintf(os.Stderr, "[coap-cli] PeerKey: %x\n", resp.PeerPublicKey)
		}
	}

	fmt.Printf("Code: %d\n", resp.Code)
	if len(resp.Body) > 0 {
		fmt.Printf("Body: %s\n", string(resp.Body))
	}
}
