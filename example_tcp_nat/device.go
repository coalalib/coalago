package main

import (
	"fmt"
	"net"
	"time"

	"github.com/coalalib/coalago"
	"github.com/google/uuid"
)

func testTCP(addr, proxy string) {
	cl := coalago.NewTCPClient(coalago.WithPrivateKey([]byte("ggg")))

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetProxy("coaps+tcp", addr)
	msg.SetSchemeCOAPS()
	msg.SetURIPath("/info")

	rsp, err := cl.Send(msg, proxy)
	if err != nil {
		fmt.Println("Send error:", err)
	}

	fmt.Println("Response:", string(rsp.Body))
}

func test(addr, proxy string) {
	cl := coalago.NewClient(coalago.WithPrivateKey([]byte("vvv")))

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetProxy("coaps", addr)
	msg.SetSchemeCOAPS()
	msg.SetURIPath("/info")

	rsp, err := cl.Send(msg, proxy)
	if err != nil {
		fmt.Println("Send error:", err)
		return
	}

	fmt.Println("Response:", string(rsp.Body))
}

func device(addr string) {
	id := uuid.New().String()

	conn, err := net.Dial("tcp", addr+":5858")
	if err != nil {
		panic(err)
	}

	defer conn.Close()
	fmt.Println("init " + id)

	server := coalago.NewServer()
	server.SetPrivateKey([]byte("555"))

	server.GET("/info", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Print("info query")
		return coalago.NewResponse(coalago.NewStringPayload("test device with id "+id), coalago.CoapCodeContent)
	})

	go server.HandleTCPConn(conn)

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetURIPath("/ping")

	time.Sleep(time.Second * 2)

	for {
		if _, err := server.Send(msg, conn.RemoteAddr().String()); err != nil {
			fmt.Println("Send error:", err)
			return
		}

		time.Sleep(15 * time.Second)
	}
}

func deviceUDP(addr string) {
	id := uuid.New().String()

	fmt.Println("init " + id)

	server := coalago.NewServer()
	server.SetPrivateKey([]byte("1234567890"))

	server.GET("/info", func(message *coalago.CoAPMessage) *coalago.CoAPResourceHandlerResult {
		fmt.Print("info query")
		return coalago.NewResponse(coalago.NewStringPayload("test device with id "+id), coalago.CoapCodeContent)
	})

	go server.Listen(":8890")

	msg := coalago.NewCoAPMessage(coalago.CON, coalago.GET)
	msg.SetURIPath("/ping")

	time.Sleep(time.Second * 2)

	for {
		if _, err := server.Send(msg, addr+":5858"); err != nil {
			fmt.Println("Send error:", err)
			return
		}

		time.Sleep(15 * time.Second)
	}
}
