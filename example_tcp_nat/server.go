package main

import (
	"flag"
	"fmt"
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
	case "clientUDP":
		deviceUDP(*addr)
	case "test":
		if *proxy == "" {
			fmt.Println("Usage: main --mode test --proxy <proxy_address>")
			os.Exit(1)
		}

		test(*addr, *proxy)
		return
	case "test2":
		if *proxy == "" {
			fmt.Println("Usage: main --mode test2 --proxy <proxy_address>")
			os.Exit(1)
		}

		testTCP(*addr, *proxy)
		return
	default:
	}

	server := coalago.NewServer()

	server.GET("/ping", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Println(message.Sender)
		go func() {
			msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
			msg.SetURIPath("/info")
			msg.SetSchemeCOAPS()

			rsp, err := server.Send(msg, message.Sender.String())
			if err != nil {
				fmt.Println("send error:", err.Error())
				return
			}

			fmt.Println(rsp.Payload.String())
		}()

		return coalago.NewResponse(coalago.NewStringPayload("pong"), coalago.CoapCodeContent)
	})

	server.GET("/session", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		return coalago.NewResponse(coalago.NewStringPayload("session"), coalago.CoapCodeContent)
	})

	go func() {
		if err := server.Listen(":5858"); err != nil {
			fmt.Println("udp:", err.Error())
		}
	}()

	panic(server.ListenTCP(":5858"))
}
