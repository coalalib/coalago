//go:build unix

package coalago

import (
	"encoding/binary"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Бенчмарки горячих путей библиотеки.
//
// Основная нагрузка в проде — keep-alive роутеров в GUMService: CON POST
// coap://<gum>/a?cid=<cid> раз в 30 секунд с каждого устройства (~25 тыс. сообщений в
// секунду на ноду), ответ 2.04 с адресом отправителя в теле. Pipeline-бенчмарки гоняют
// настоящий listenLoop поверх транспорта в памяти: системных вызовов нет, клиентская
// сторона не аллоцирует, поэтому allocs/op и B/op — это затраты сервера на одно сообщение.
// UDP-бенчмарки меряют то же через loopback вместе с сокетами и планировщиком.

const benchCID = "2Hc8xQ4p0bVnR3sD7kLm9w"

// aliveHandler повторяет работу GUM HandlerAlive с сообщением: cid из query и адрес
// отправителя в ответе.
func aliveHandler(m *CoAPMessage) *CoAPResourceHandlerResult {
	if m.GetURIQuery("cid") == "" {
		return NewResponse(NewStringPayload("wrong cid"), CoapCodeBadRequest)
	}
	return NewResponse(NewStringPayload(m.Sender.String()), CoapCodeChanged)
}

func newAliveRequest() *CoAPMessage {
	m := NewCoAPMessage(CON, POST)
	m.Token = make([]byte, 8)
	m.SetURIPath("/a")
	m.SetURIQuery("cid", benchCID)
	return m
}

// putBenchToken пишет в 8-байтовый токен сериализованного запроса номер слота окна и
// порядковый номер запроса: токены уникальны (дедупликация не отбрасывает запросы), а по
// токену ответа видно, какой слот освободился.
func putBenchToken(datagram []byte, slot int, seq uint64) {
	binary.BigEndian.PutUint16(datagram[4:6], uint16(slot))
	binary.BigEndian.PutUint32(datagram[6:10], uint32(seq))
	binary.BigEndian.PutUint16(datagram[10:12], uint16(seq>>32))
}

// processCPU — процессорное время процесса (user+sys) со всех ядер. Для сервера это
// главная метрика: «сколько CPU стоит одно сообщение», включая GC и планировщик.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		panic(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func reportCPU(b *testing.B, start time.Duration) {
	b.ReportMetric(float64(processCPU()-start)/float64(b.N), "cpu-ns/op")
}

func benchSenders(n int) []*net.UDPAddr {
	senders := make([]*net.UDPAddr, n)
	for i := range senders {
		senders[i] = &net.UDPAddr{
			IP:   net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)).To4(),
			Port: 40000 + i%20000,
		}
	}
	return senders
}

// runPipeline прогоняет b.N запросов через сервер, держа в полёте не больше window
// запросов. template — сериализованный запрос с 8-байтовым токеном.
func runPipeline(b *testing.B, mt *memTransport, template []byte, senders []*net.UDPAddr) {
	const window = 256
	slots := make(chan int, window)
	bufs := make([][]byte, window)
	for i := range window {
		bufs[i] = append([]byte(nil), template...)
		slots <- i
	}

	var mismatched atomic.Int64
	mt.onWrite = func(buf []byte) {
		if len(buf) < 12 || buf[0]&0x0f != 8 {
			mismatched.Add(1)
			return
		}
		slots <- int(binary.BigEndian.Uint16(buf[4:6]))
	}

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	heapBefore := ms.HeapAlloc
	b.ReportAllocs()
	b.ResetTimer()
	cpu := processCPU()
	for i := 0; i < b.N; i++ {
		slot := <-slots
		putBenchToken(bufs[slot], slot, benchSeq.Add(1))
		mt.in <- memDatagram{b: bufs[slot], from: senders[i%len(senders)]}
	}
	for range window {
		<-slots
	}
	b.StopTimer()
	reportCPU(b, cpu)

	if n := mismatched.Load(); n > 0 {
		b.Fatalf("%d unexpected writes from server", n)
	}
	// Прирост живой кучи после GC на одно сообщение: всё, что библиотека держит после
	// обмена (дедупликация, кэши адресов и состояний), — работа разметки в каждом цикле GC.
	runtime.GC()
	runtime.ReadMemStats(&ms)
	b.ReportMetric((float64(ms.HeapAlloc)-float64(heapBefore))/float64(b.N), "live-B/op")
}

// BenchmarkServerPipelineAlive — /a по открытому coap, как шлёт coalagent.
func BenchmarkServerPipelineAlive(b *testing.B) {
	s := NewServer()
	s.POST("/a", aliveHandler)
	mt := startMemServer(b, s)

	template, err := Serialize(newAliveRequest())
	if err != nil {
		b.Fatal(err)
	}
	runPipeline(b, mt, template, benchSenders(4096))
}

// BenchmarkServerPipelineCoaps — тот же /a, но по coaps с уже установленной сессией:
// так ходят запросы мобильных приложений и прокси в AccountService и DataService.
func BenchmarkServerPipelineCoaps(b *testing.B) {
	s := NewServer()
	s.POST("/a", aliveHandler)
	mt := startMemServer(b, s)

	srvSession, cliSession := sessionPair(b)
	senders := benchSenders(1024)
	local := mt.LocalAddr().String()
	for _, a := range senders {
		s.sessions.Set(local, a.String(), "", srvSession)
	}

	req := newAliveRequest()
	req.SetSchemeCOAPS()
	if err := encrypt(req, local, cliSession.AEAD); err != nil {
		b.Fatal(err)
	}
	template, err := Serialize(req)
	if err != nil {
		b.Fatal(err)
	}
	runPipeline(b, mt, template, senders)
}

func BenchmarkDeserializeAlive(b *testing.B) {
	data, err := Serialize(newAliveRequest())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Deserialize(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSerializeAliveResponse(b *testing.B) {
	req := newAliveRequest()
	req.Sender = &net.UDPAddr{IP: net.IPv4(10, 1, 2, 3).To4(), Port: 41234}
	b.ReportAllocs()
	for b.Loop() {
		resp := NewCoAPMessageId(ACK, CoapCodeChanged, req.MessageID)
		resp.Payload = NewStringPayload("10.1.2.3:41234")
		resp.Token = req.Token
		if _, err := Serialize(resp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkServerAliveUDP — /a через loopback: 32 «роутера» с собственными сокетами шлют
// запросы без пауз. Клиенты не аллоцируют; потерянный ответ перепосылается с новым токеном
// (старый токен сервер уже отбрасывает как повтор).
func BenchmarkServerAliveUDP(b *testing.B) {
	s := NewServer()
	s.POST("/a", aliveHandler)
	addr := startUDPServer(b, s)

	template, err := Serialize(newAliveRequest())
	if err != nil {
		b.Fatal(err)
	}

	const clients = 32
	conns := make([]*net.UDPConn, clients)
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		b.Fatal(err)
	}
	for i := range conns {
		if conns[i], err = net.DialUDP("udp", nil, raddr); err != nil {
			b.Fatal(err)
		}
		defer conns[i].Close()
	}

	var next, retries atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	cpu := processCPU()
	var wg sync.WaitGroup
	for c := range clients {
		wg.Add(1)
		go func(c int, conn *net.UDPConn) {
			defer wg.Done()
			req := append([]byte(nil), template...)
			resp := make([]byte, MTU+1)
			for next.Add(1) <= int64(b.N) {
				for {
					putBenchToken(req, c, benchSeq.Add(1))
					if _, err := conn.Write(req); err != nil {
						b.Error(err)
						return
					}
					if waitToken(conn, resp, req[4:12]) {
						break
					}
					retries.Add(1)
				}
			}
		}(c, conns[c])
	}
	wg.Wait()
	b.StopTimer()
	reportCPU(b, cpu)
	b.ReportMetric(float64(retries.Load()), "retries")
}

// waitToken читает ответы, пока не придёт ответ с нужным токеном; false — таймаут.
func waitToken(conn *net.UDPConn, buf, token []byte) bool {
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return false
		}
		if n >= 12 && string(buf[4:12]) == string(token) {
			return true
		}
	}
}

// BenchmarkClientCoapsGET — клиент с переиспользованием сокета (как telemetry-клиент
// gum-server) делает coaps GET к серверу на loopback. Считает клиента и сервер вместе.
func BenchmarkClientCoapsGET(b *testing.B) {
	s := NewServer()
	s.GET("/probe", func(*CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewStringPayload(`{"status":"ok"}`), CoapCodeContent)
	})
	uri := fmt.Sprintf("coaps://%s/probe?cid=%s", startUDPServer(b, s), benchCID)

	c := NewClient(WithIdleConns(4))
	b.Cleanup(func() { _ = c.Close() })
	if _, err := c.GET(uri); err != nil { // handshake вне замера
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rsp, err := c.GET(uri)
		if err != nil {
			b.Fatal(err)
		}
		if rsp.Code != CoapCodeContent {
			b.Fatalf("code %v", rsp.Code)
		}
	}
}

// BenchmarkBlockwise64K — блочная передача 64 КБ в обе стороны (Block1 запрос, Block2 ответ).
func BenchmarkBlockwise64K(b *testing.B) {
	body := make([]byte, 64<<10)
	for i := range body {
		body[i] = byte(i)
	}
	s := NewServer()
	s.POST("/echo", func(m *CoAPMessage) *CoAPResourceHandlerResult {
		return NewResponse(NewBytesPayload(m.Payload.Bytes()), CoapCodeChanged)
	})
	uri := fmt.Sprintf("coap://%s/echo", startUDPServer(b, s))

	c := NewClient()
	b.ReportAllocs()
	b.SetBytes(int64(2 * len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// У клиента нет общего дедлайна обмена: если сервер так и не ответит на последний
		// блок, POST крутится вечно, а -timeout на бенчмарки не действует.
		hang := time.AfterFunc(20*time.Second, func() {
			panic("block transfer hung: all blocks acked, final response never came")
		})
		rsp, err := c.POST(body, uri)
		hang.Stop()
		if err != nil {
			b.Fatal(err)
		}
		if len(rsp.Body) != len(body) {
			b.Fatalf("echo %d bytes, want %d", len(rsp.Body), len(body))
		}
	}
}
