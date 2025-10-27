package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/coalalib/coalago"
)

const (
	ipv6Port = 5683
	ipv6Addr = "[::1]" // IPv6 localhost
)

func main() {
	mode := flag.String("mode", "", "server|client")
	addr := flag.String("addr", ipv6Addr, "IPv6 address to bind/connect to")
	port := flag.Int("port", ipv6Port, "Port to use")
	flag.Parse()

	if *mode == "" {
		fmt.Println("Usage: go run main.go --mode server|client [--addr <ipv6_addr>] [--port <port>]")
		fmt.Println("Examples:")
		fmt.Println("  Server: go run main.go --mode server")
		fmt.Println("  Client: go run main.go --mode client --addr [::1]")
		os.Exit(1)
	}

	// Проверяем что адрес является IPv6
	if !isIPv6(*addr) {
		log.Fatalf("Address %s is not a valid IPv6 address", *addr)
	}

	// Убираем квадратные скобки если они есть в адресе
	cleanAddr := *addr
	if len(cleanAddr) > 2 && cleanAddr[0] == '[' && cleanAddr[len(cleanAddr)-1] == ']' {
		cleanAddr = cleanAddr[1 : len(cleanAddr)-1]
	}
	fullAddr := fmt.Sprintf("[%s]:%d", cleanAddr, *port)

	switch *mode {
	case "server":
		startIPv6Server(fullAddr)
	case "client":
		startIPv6Client(fullAddr)
	default:
		fmt.Println("Invalid mode. Use 'server' or 'client'")
		os.Exit(1)
	}
}

func isIPv6(addr string) bool {
	// Убираем квадратные скобки если есть
	if len(addr) > 2 && addr[0] == '[' && addr[len(addr)-1] == ']' {
		addr = addr[1 : len(addr)-1]
	}
	return net.ParseIP(addr) != nil && net.ParseIP(addr).To16() != nil
}

func startIPv6Server(addr string) {
	fmt.Printf("Starting IPv6 CoAP server on %s\n", addr)

	server := coalago.NewServer()

	// Обработчик для получения информации о сервере
	server.GET("/info", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Printf("Received GET /info from %s\n", message.Sender)
		response := fmt.Sprintf("IPv6 CoAP Server is running on %s", addr)
		return coalago.NewResponse(coalago.NewStringPayload(response), coalago.CoapCodeContent)
	})

	// Обработчик для эха
	server.POST("/echo", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Printf("Received POST /echo from %s, payload: %s\n", message.Sender, string(message.Payload.Bytes()))
		return coalago.NewResponse(message.Payload, coalago.CoapCodeContent)
	})

	// Обработчик для получения времени
	server.GET("/time", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Printf("Received GET /time from %s\n", message.Sender)
		currentTime := time.Now().Format(time.RFC3339)
		return coalago.NewResponse(coalago.NewStringPayload(currentTime), coalago.CoapCodeContent)
	})

	// Обработчик для проверки IPv6 соединения
	server.GET("/ipv6", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Printf("Received GET /ipv6 from %s\n", message.Sender)
		response := fmt.Sprintf("IPv6 connection confirmed from %s, coaps: ", message.Sender)
		return coalago.NewResponse(coalago.NewStringPayload(response), coalago.CoapCodeContent)
	})

	fmt.Printf("Server listening on IPv6 address: %s\n", addr)
	if err := server.Listen(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func startIPv6Client(addr string) {
	fmt.Printf("Connecting to IPv6 CoAP server at %s\n", addr)

	client := coalago.NewClient()

	// Тест 1: Получение информации о сервере
	fmt.Println("\n=== Test 1: GET /info ===")
	resp, err := client.GET(fmt.Sprintf("coaps://%s/info", addr))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Response: %s\n", string(resp.Body))
	}

	// Тест 2: Эхо тест
	fmt.Println("\n=== Test 2: POST /echo ===")
	testPayload := "Hello IPv6 CoAP!"
	resp, err = client.POST([]byte(testPayload), fmt.Sprintf("coaps://%s/echo", addr))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Sent: %s\n", testPayload)
		fmt.Printf("Received: %s\n", string(resp.Body))
	}

	// Тест 3: Получение времени
	fmt.Println("\n=== Test 3: GET /time ===")
	resp, err = client.GET(fmt.Sprintf("coaps://%s/time", addr))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Server time: %s\n", string(resp.Body))
	}

	// Тест 4: Проверка IPv6 соединения
	fmt.Println("\n=== Test 4: GET /ipv6 ===")
	resp, err = client.GET(fmt.Sprintf("coaps://%s/ipv6", addr))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("IPv6 test result: %s\n", string(resp.Body))
	}

	fmt.Println("\nAll IPv6 CoAP tests completed!")
}
