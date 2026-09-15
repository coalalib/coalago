package coalago

type Opt func(*coalaopts)

func WithPrivateKey(privatekey []byte) Opt {
	return func(opts *coalaopts) {
		opts.privatekey = privatekey
	}
}

// WithIdleConns включает переиспользование сокетов: клиент держит до n простаивающих
// сокетов на адрес пира и берёт их для следующих запросов.
//
// Зачем: без этого каждый запрос открывает сокет на новом эфемерном порту, а кэш сессий
// ключуется по (локальный адрес, адрес пира) - то есть не попадает ни разу. Это полный
// handshake на каждый запрос и по записи в таблице сессий пира на каждый порт; стороны
// регулярно расходятся, и обмен падает с session expired.
//
// По умолчанию 0 - прежнее поведение, сокет закрывается после обмена. Клиенту с n > 0
// нужен долгий срок жизни или Close(): иначе сокеты останутся открытыми до конца
// процесса. Для TCP-клиента параметр игнорируется.
func WithIdleConns(n int) Opt {
	return func(opts *coalaopts) {
		opts.idleConns = n
	}
}

type coalaopts struct {
	privatekey []byte
	idleConns  int
}
