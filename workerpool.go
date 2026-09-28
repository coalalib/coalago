package coalago

import "sync/atomic"

// Входящие сообщения обрабатывают переиспользуемые горутины, а не новая горутина на
// каждое сообщение (раньше их было даже две: разбор и отдельно обработчик). Создание
// горутины и рост её стека под глубокий путь обработки повторялись на каждом пакете —
// на профиле GUMService это ~9 % CPU в morestack/copystack и ~8 % в планировщике.
//
// Параллелизм пул не ограничивает: если свободной горутины нет, запускается новая, как
// и раньше, поэтому медленный обработчик не задерживает остальные сообщения.
// Освободившаяся горутина ждёт следующее сообщение; ждущих держится не больше
// maxIdleWorkers на процесс, лишние завершаются сразу. Пул общий для всех серверов
// процесса: у ghost их тысячи, и простаивающие горутины на каждый сервер были бы
// лишними.

const maxIdleWorkers = 1024

var workers = &workerPool{tasks: make(chan rxTask)}

// rxTask — входящее сообщение сервера. Передаётся по каналу значением, без замыкания и
// аллокации на сообщение.
type rxTask struct {
	s   *Server
	tr  *transport
	msg *CoAPMessage
	// sem — слот семафора listenLoop, освобождается после приёма сообщения, до запуска
	// обработчика; nil — без семафора (TCP, ServeMessage).
	sem chan struct{}
	// proxy — переслать сообщение по Proxy-URI вместо локальной обработки.
	proxy bool
}

func (t *rxTask) run() {
	if t.proxy {
		t.s.forwardProxy(t.msg, t.tr, t.sem)
		return
	}
	t.s.handleMessage(t.msg, t.tr, t.sem)
}

type workerPool struct {
	tasks chan rxTask
	idle  atomic.Int32
}

func (p *workerPool) submit(t rxTask) {
	select {
	case p.tasks <- t:
	default:
		go p.work(t)
	}
}

func (p *workerPool) work(t rxTask) {
	for {
		t.run()
		// Ждущая горутина не должна держать последнее сообщение живым для GC.
		t = rxTask{}
		if p.idle.Add(1) > maxIdleWorkers {
			p.idle.Add(-1)
			return
		}
		t = <-p.tasks
		p.idle.Add(-1)
	}
}

func releaseSlot(sem chan struct{}) {
	if sem != nil {
		<-sem
	}
}
