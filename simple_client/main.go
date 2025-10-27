package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/coalalib/coalago"
)

func main() {
	// Определяем флаги командной строки
	method := flag.String("method", "GET", "HTTP метод: GET, POST, DELETE")
	uri := flag.String("uri", "", "URI сервера (например: coap://127.0.0.1:5683/test)")
	transport := flag.String("transport", "udp", "Транспорт: udp или tcp")
	privateKey := flag.String("key", "", "Приватный ключ для шифрования (опционально)")
	data := flag.String("data", "", "Данные для отправки (для POST)")
	proxyAddr := flag.String("proxy", "", "Адрес прокси сервера (например: 127.0.0.1:5858)")
	proxyScheme := flag.String("proxy-scheme", "", "Схема прокси: coaps, coaps+tcp (если не указана - определяется автоматически)")

	flag.Parse()

	// Проверяем обязательные параметры
	if *uri == "" {
		fmt.Println("Ошибка: необходимо указать URI")
		flag.Usage()
		os.Exit(1)
	}

	// Создаем клиент в зависимости от транспорта
	var client *coalago.Client
	if *transport == "tcp" {
		// TCP клиент для coap+tcp:// или coaps+tcp://
		if *privateKey != "" {
			client = coalago.NewTCPClient(coalago.WithPrivateKey([]byte(*privateKey)))
		} else {
			client = coalago.NewTCPClient()
		}
	} else {
		// UDP клиент для coap:// или coaps://
		if *privateKey != "" {
			client = coalago.NewClient(coalago.WithPrivateKey([]byte(*privateKey)))
		} else {
			client = coalago.NewClient()
		}
	}

	// Если указан прокси - используем низкоуровневый API с ручным созданием сообщения
	if *proxyAddr != "" {
		resp, err := sendViaProxy(client, *method, *uri, *data, *proxyAddr, *proxyScheme, *transport)
		if err != nil {
			fmt.Printf("Ошибка при выполнении запроса через прокси: %v\n", err)
			os.Exit(1)
		}
		printResponse(resp)
		return
	}

	// Выполняем запрос напрямую без прокси
	var resp *coalago.Response
	var err error

	switch *method {
	case "GET":
		resp, err = client.GET(*uri)
	case "POST":
		resp, err = client.POST([]byte(*data), *uri)
	case "DELETE":
		resp, err = client.DELETE([]byte(*data), *uri)
	default:
		fmt.Printf("Неподдерживаемый метод: %s. Используйте GET, POST или DELETE\n", *method)
		os.Exit(1)
	}

	// Обрабатываем ошибки
	if err != nil {
		fmt.Printf("Ошибка при выполнении запроса: %v\n", err)
		os.Exit(1)
	}

	printResponse(resp)
}

// sendViaProxy отправляет запрос через прокси сервер
func sendViaProxy(client *coalago.Client, method, uri, data, proxyAddr, proxyScheme, transport string) (*coalago.Response, error) {
	// Определяем код метода
	var code coalago.CoapCode
	switch method {
	case "GET":
		code = coalago.GET
	case "POST":
		code = coalago.POST
	case "DELETE":
		code = coalago.DELETE
	default:
		return nil, fmt.Errorf("неподдерживаемый метод: %s", method)
	}

	// Создаем сообщение вручную
	msg := coalago.NewCoAPMessage(coalago.CON, code)

	// Устанавливаем схему в зависимости от URI
	if len(uri) > 6 && uri[:6] == "coaps:" {
		msg.SetSchemeCOAPS()
	} else {
		msg.SetSchemeCOAP()
	}

	// Определяем схему прокси автоматически если не указана
	if proxyScheme == "" {
		if transport == "tcp" {
			proxyScheme = "coaps+tcp"
		} else {
			proxyScheme = "coaps"
		}
	}

	// Устанавливаем прокси
	msg.SetProxy(proxyScheme, proxyAddr)

	// Парсим URI для извлечения пути
	msg.SetURIPath(extractPath(uri))

	// Добавляем данные если это POST
	if method == "POST" && data != "" {
		msg.Payload = coalago.NewBytesPayload([]byte(data))
	}

	// Отправляем через прокси
	return client.Send(msg, proxyAddr)
}

// extractPath извлекает путь из URI
func extractPath(uri string) string {
	// Простой парсинг пути из URI
	// Убираем схему
	start := 0
	if idx := findIndex(uri, "://"); idx != -1 {
		start = idx + 3
	}

	// Находим начало пути
	for i := start; i < len(uri); i++ {
		if uri[i] == '/' {
			return uri[i:]
		}
	}

	return "/"
}

// findIndex находит индекс подстроки в строке
func findIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// printResponse выводит ответ в консоль
func printResponse(resp *coalago.Response) {
	fmt.Println("=== Ответ от сервера ===")
	fmt.Printf("Код ответа: %s\n", resp.Code.String())
	fmt.Printf("Тело ответа: %s\n", string(resp.Body))

	if len(resp.PeerPublicKey) > 0 {
		fmt.Printf("Публичный ключ сервера: %x\n", resp.PeerPublicKey)
	}
}
