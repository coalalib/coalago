package main

import (
	"fmt"
	"net"
	"time"

	"github.com/coalalib/coalago"
	"github.com/google/uuid"
)

func test(addr, proxy string) {
	conn, err := net.Dial("tcp", addr+":5858")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	fmt.Println("Connected to server")

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetProxy("coap+tcp", proxy)
	msg.SetURIPath("/info")

	b, _ := coalago.Serialize(msg)
	coalago.WriteTcpFrame(conn, b)
}

func device(addr string) {
	id := uuid.New().String()

	conn, err := net.Dial("tcp", addr+":5858")
	if err != nil {
		panic(err)
	}

	defer conn.Close()
	fmt.Println("Connected to server")

	server := coalago.NewServer()

	server.GET("/info", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Print("info query")
		return coalago.NewResponse(coalago.NewStringPayload("test device with id "+id), coalago.CoapCodeContent)
	})

	go server.HandleTCPConn(conn)

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetURIPath("/ping")

	time.Sleep(time.Second)

	for {
		if _, err := server.Send(msg, conn.RemoteAddr().String()); err != nil {
			fmt.Println("Send error:", err)
			return
		}

		time.Sleep(15 * time.Second)
	}
}
