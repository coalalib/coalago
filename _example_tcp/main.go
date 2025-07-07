package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coalalib/coalago"
)

const (
	portForTest             = 1111
	pathTestBlock1          = "/testblock1"
	MAX_PAYLOAD_SIZE        = 1024
	expectedResponseMessage = "Hello from Coala!:)"
)

func main() {
	mode := flag.String("mode", "", "server|client")
	useTCP := flag.Bool("tcp", false, "Use TCP instead of UDP")
	flag.Parse()

	if *mode == "" {
		fmt.Println("Usage: main --mode server|client [--tcp]")
		os.Exit(1)
	}

	stringPayload := strings.Repeat("a", 100)
	expectedPayload := []byte(stringPayload)
	expectedResponse := []byte(expectedResponseMessage)

	switch *mode {
	case "server":
		startServer(expectedPayload, expectedResponse, *useTCP)
	case "client":
		startClient(expectedPayload, expectedResponse, *useTCP)
	default:
		fmt.Println("Invalid mode. Use --mode server|client")
		os.Exit(1)
	}
}

func startServer(expectedPayload, expectedResponse []byte, useTCP bool) {
	s := coalago.NewServer()
	s.POST(pathTestBlock1, func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Println("[SERVER] got request, payload size:", len(message.Payload.Bytes()))
		if !bytes.Equal(message.Payload.Bytes(), expectedPayload) {
			panic(fmt.Sprintf("Expected payload: %s\n\nActual payload: %s\n", expectedPayload, message.Payload.Bytes()))
		}
		return coalago.NewResponse(coalago.NewBytesPayload(expectedResponse), coalago.CoapCodeChanged)
	})

	addr := fmt.Sprintf(":%d", portForTest)
	fmt.Printf("Server listening on %s (TCP: %v)\n", addr, useTCP)
	panic(s.ListenTCP(addr))
}

func startClient(expectedPayload, expectedResponse []byte, useTCP bool) {
	c := coalago.NewClient()
	var wg sync.WaitGroup
	var count int32
	scheme := "coap"
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fmt.Println("[CLIENT] sending request...")
			url := fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, portForTest, pathTestBlock1)
			resp, err := c.POST(expectedPayload, url)
			if err != nil {
				fmt.Println("[CLIENT] error:", err)
				atomic.AddInt32(&count, 1)
				return
			}
			fmt.Println("[CLIENT] got response, size:", len(resp.Body))
			if !bytes.Equal(resp.Body, expectedResponse) {
				panic(fmt.Sprintf("Expected response: %s\n\nActual response: %s\n", expectedResponse, resp.Body))
			}
		}()
	}
	wg.Wait()
	fmt.Printf("Done. Errors: %d\n", count)
}
