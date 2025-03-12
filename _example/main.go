package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coalalib/coalago"
)

const (
	portForTest      int    = 1111
	pathTestBlock1          = "/testblock1"
	pathTestBlock2          = "/testblock2"
	pathTestBlockMix        = "/testblockmix"
	MAX_PAYLOAD_SIZE        = 1024
	expectedResponseMessage = "Hello from Coala!:)"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: main <mode>")
		os.Exit(1)
	}
	mode := os.Args[1]

	stringPayload := strings.Repeat("a", 100*MAX_PAYLOAD_SIZE)
	expectedPayload := []byte(stringPayload)
	expectedResponse := []byte(expectedResponseMessage)

	switch mode {
	case "server":
		startServer(expectedPayload, expectedResponse)
	case "client":
		startClient(expectedPayload, expectedResponse)
	default:
		fmt.Println("Invalid mode. Use 'server' or 'client'.")
		os.Exit(1)
	}
}

func startServer(expectedPayload, expectedResponse []byte) {
	s := coalago.NewServer()
	s.POST(pathTestBlock1, func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		if !bytes.Equal(message.Payload.Bytes(), expectedPayload) {
			panic(fmt.Sprintf("Expected payload: %s\n\nActual payload: %s\n", expectedPayload, message.Payload.Bytes()))
		}

		resp := coalago.NewBytesPayload(expectedResponse)
		return coalago.NewResponse(resp, coalago.CoapCodeChanged)
	})
	panic(s.Listen(fmt.Sprintf(":%d", portForTest)))
}

func startClient(expectedPayload, expectedResponse []byte) {
	c := coalago.NewClient()
	var wg sync.WaitGroup
	var count int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := c.POST(expectedPayload, fmt.Sprintf("coap://127.0.0.1:%d%s", portForTest, pathTestBlock1))
			if err != nil {
				fmt.Println(err)
				atomic.AddInt32(&count, 1)
				return
			}

			if !bytes.Equal(resp.Body, expectedResponse) {
				panic(fmt.Sprintf("Expected response: %s\n\nActual response: %s\n", expectedResponse, resp.Body))
			}
		}()
	}
	wg.Wait()
}
