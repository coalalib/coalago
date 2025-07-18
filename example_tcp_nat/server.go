package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/coalalib/coalago"
)

var deviceConns sync.Map // deviceID -> net.Conn

func main() {
	mode := flag.String("mode", "", "server|client")
	addr := flag.String("addr", "", "address")
	proxy := flag.String("proxy", "", "proxy address")
	flag.Parse()

	if *mode == "" {
		fmt.Println("Usage: main --mode server|client [--tcp]")
		os.Exit(1)
	}

	if addr == nil && (*mode == "client" || *mode == "test") {
		fmt.Println("Usage: main --mode client|test --addr <device_address>")
		os.Exit(1)
	}

	switch *mode {
	case "client":
		device(*addr)
	case "test":
		if *proxy == "" {
			fmt.Println("Usage: main --mode test --proxy <proxy_address>")
			os.Exit(1)
		}

		test(*addr, *proxy)
		return
	default:
	}

	server := coalago.NewServer()

	server.GET("/ping", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Println(message.Sender)
		return coalago.NewResponse(coalago.NewStringPayload("pong"), coalago.CoapCodeContent)
	})

	server.GET("/session", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		return coalago.NewResponse(coalago.NewStringPayload("session"), coalago.CoapCodeContent)
	})

	panic(server.ListenTCP(":5858"))
}

func sendCommandToDevice(deviceID string) {
	val, ok := deviceConns.Load(deviceID)
	if !ok {
		fmt.Println("Device not found")
		return
	}
	conn := val.(net.Conn)
	coapMsg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)

	coapMsg.SetURIPath("/status")
	data, _ := coalago.Serialize(coapMsg)
	_, err := coalago.WriteTcpFrame(conn, data)
	if err != nil {
		fmt.Println("writeFrame error:", err)
	}
}
