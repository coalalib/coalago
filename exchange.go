package coalago

import (
	"hash/maphash"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
	"weak"
)

// Обмен (exchange) — запрос пира и всё, что к нему относится: повторы, блоки, ответ.
// Обмен определяется парой (адрес пира, токен), как и раньше; раньше ключом была строка
// Sender.String()+токен, и её сборка стоила ~4 аллокации и форматирование адреса на
// каждое сообщение, да ещё по нескольку раз. Теперь ключ — структура без указателей: её
// сборка не аллоцирует, а таблицы дедупликации, где живут сотни тысяч записей, сборщик
// мусора не сканирует.

// peerKey — адрес пира. Равенство ключей совпадает с равенством Sender.String():
// IPv4, пришедший как IPv4-mapped IPv6, даёт тот же ключ, что и просто IPv4.
type peerKey struct {
	ip   [16]byte
	zone uint64 // хэш зоны IPv6; для адресов не-IP типов — вторая половина хэша строки
	port uint16
	kind uint8
}

const (
	peerIPv4 uint8 = iota + 1
	peerIPv6
	peerOther // адрес без IP (не UDP/TCP либо не разбирается): ключ — 128-битный хэш String()
)

// exKey — ключ обмена: пир и токен. Токены длиннее 8 байт (в CoAP не бывает, но
// SetToken позволяет) сворачиваются в хэш с отдельной меткой длины.
type exKey struct {
	peer peerKey
	tok  [8]byte
	tlen uint8
}

const longTokenLen = 0xff

var keySeed, keySeed2 = maphash.MakeSeed(), maphash.MakeSeed()

func peerKeyOf(addr net.Addr) peerKey {
	switch a := addr.(type) {
	case *net.UDPAddr:
		if a != nil {
			if k, ok := peerKeyIP(a.IP, a.Port, a.Zone); ok {
				return k
			}
		}
	case *net.TCPAddr:
		if a != nil {
			if k, ok := peerKeyIP(a.IP, a.Port, a.Zone); ok {
				return k
			}
		}
	}
	if addr == nil {
		return peerKey{}
	}
	return peerKeyString(addr.String())
}

func peerKeyIP(ip net.IP, port int, zone string) (peerKey, bool) {
	var k peerKey
	if port < 0 || port > 0xffff {
		return k, false
	}
	k.port = uint16(port)
	if zone != "" {
		k.zone = maphash.String(keySeed, zone) | 1
	}
	if ip4 := ip.To4(); ip4 != nil {
		k.kind = peerIPv4
		copy(k.ip[:], ip4)
		return k, true
	}
	if len(ip) != net.IPv6len {
		return k, false
	}
	k.kind = peerIPv6
	copy(k.ip[:], ip)
	return k, true
}

// peerKeyString — ключ адреса, заданного строкой (адрес назначения прокси, адрес в
// Server.Send). Строка ip:port даёт тот же ключ, что и *net.UDPAddr с этим адресом.
func peerKeyString(s string) peerKey {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		a := ap.Addr().Unmap()
		k := peerKey{port: ap.Port()}
		if a.Is4() {
			k.kind = peerIPv4
			ip4 := a.As4()
			copy(k.ip[:], ip4[:])
			return k
		}
		k.kind = peerIPv6
		k.ip = a.As16()
		if z := a.Zone(); z != "" {
			k.zone = maphash.String(keySeed, z) | 1
		}
		return k
	}
	k := peerKey{kind: peerOther, zone: maphash.String(keySeed2, s)}
	h := maphash.String(keySeed, s)
	for i := range 8 {
		k.ip[i] = byte(h >> (8 * i))
	}
	return k
}

func exKeyOf(peer peerKey, token []byte) exKey {
	k := exKey{peer: peer}
	if len(token) <= len(k.tok) {
		k.tlen = uint8(len(token))
		copy(k.tok[:], token)
		return k
	}
	k.tlen = longTokenLen
	h := maphash.Bytes(keySeed, token)
	for i := range k.tok {
		k.tok[i] = byte(h >> (8 * i))
	}
	return k
}

func messageKey(m *CoAPMessage) exKey {
	return exKeyOf(peerKeyOf(m.Sender), m.Token)
}

// monoNow — монотонное время процесса в наносекундах: сроки в таблицах не зависят от
// перевода системных часов.
var monoBase = time.Now()

func monoNow() int64 {
	return int64(time.Since(monoBase))
}

const tableShards = 64

// exchangeTable — дедупликация запросов, пришедших серверу:
//   - обмен «в работе», пока выполняется обработчик: повторы отбрасываются;
//   - «обработан» ещё processedTTL после ответа: запоздалые ретрансмиты отбрасываются.
//
// Записи без указателей; просроченные вычищаются фоновым проходом каждые sweepEvery.
type exchangeTable struct {
	shards [tableShards]exchangeShard
}

type exchangeShard struct {
	mu sync.Mutex
	m  map[exKey]exchangeState
	_  [48]byte // своя кэш-линия у каждого шарда
}

type exchangeState struct {
	expires int64
	done    bool
}

const (
	// runningTTL — сколько обмен считается «в работе»: столько же жило прежнее состояние
	// обмена; обработчик дольше этого срока повторно запустит ретрансмит.
	runningTTL = 3 * time.Minute
	// processedTTL — сколько после ответа отбрасываются повторы того же запроса.
	processedTTL = 10 * time.Second
)

var exchanges = newExchangeTable(processedTTL / 2)

func newExchangeTable(sweepEvery time.Duration) *exchangeTable {
	t := &exchangeTable{}
	for i := range t.shards {
		t.shards[i].m = make(map[exKey]exchangeState)
	}
	go t.sweepLoop(sweepEvery)
	return t
}

func (t *exchangeTable) shard(k exKey) *exchangeShard {
	return &t.shards[maphash.Comparable(keySeed, k)%tableShards]
}

// processed — обмен уже обработан и повтор надо отбросить.
func (t *exchangeTable) processed(k exKey) bool {
	sh := t.shard(k)
	now := monoNow()
	sh.mu.Lock()
	e, ok := sh.m[k]
	sh.mu.Unlock()
	return ok && e.done && e.expires > now
}

// claim — забрать обмен под обработчик. false: обработчик уже запущен или обмен обработан.
func (t *exchangeTable) claim(k exKey) bool {
	sh := t.shard(k)
	now := monoNow()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.m[k]; ok && e.expires > now {
		return false
	}
	sh.m[k] = exchangeState{expires: now + int64(runningTTL)}
	return true
}

// finish — обработчик вернулся: повторы отбрасываются ещё processedTTL.
func (t *exchangeTable) finish(k exKey) {
	sh := t.shard(k)
	now := monoNow()
	sh.mu.Lock()
	sh.m[k] = exchangeState{expires: now + int64(processedTTL), done: true}
	sh.mu.Unlock()
}

func (t *exchangeTable) sweepLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for range ticker.C {
		now := monoNow()
		for i := range t.shards {
			sh := &t.shards[i]
			sh.mu.Lock()
			for k, e := range sh.m {
				if e.expires <= now {
					delete(sh.m, k)
				}
			}
			sh.mu.Unlock()
		}
	}
}

// lockTable — замки приёма по обмену. Запись живёт, пока замок держат или ждут (refs),
// поэтому замков ровно столько, сколько обменов принимается сейчас, а обмены друг
// друга не блокируют.
type lockTable struct {
	shards [tableShards]lockShard
}

type lockShard struct {
	mu sync.Mutex
	m  map[exKey]*exchangeLock
	_  [48]byte
}

type exchangeLock struct {
	mu   sync.Mutex
	refs int // под mu шарда
}

var exchangeLocks = newLockTable()

func newLockTable() *lockTable {
	t := &lockTable{}
	for i := range t.shards {
		t.shards[i].m = make(map[exKey]*exchangeLock)
	}
	return t
}

func (t *lockTable) shard(k exKey) *lockShard {
	return &t.shards[maphash.Comparable(keySeed, k)%tableShards]
}

func (t *lockTable) lock(k exKey) *exchangeLock {
	sh := t.shard(k)
	sh.mu.Lock()
	l := sh.m[k]
	if l == nil {
		l = &exchangeLock{}
		sh.m[k] = l
	}
	l.refs++
	sh.mu.Unlock()

	l.mu.Lock()
	return l
}

func (t *lockTable) unlock(k exKey, l *exchangeLock) {
	l.mu.Unlock()

	sh := t.shard(k)
	sh.mu.Lock()
	if l.refs--; l.refs == 0 {
		delete(sh.m, k)
	}
	sh.mu.Unlock()
}

// blockTable — незавершённые приёмы Block1 (большие запросы). Состояние создаётся
// атомарно: раньше два первых блока, пришедших одновременно, могли создать два разных
// состояния, блоки одного из них терялись, и передача не завершалась никогда — клиент,
// получивший ACK на все блоки, ждал ответа вечно.
type blockTable struct {
	shards [tableShards]blockShard
}

type blockShard struct {
	mu sync.Mutex
	m  map[exKey]*block1State
	_  [48]byte
}

// block1State — приём одного большого запроса. Блоки, totalBlocks и assembled меняются
// только под замком обмена (exchangeLocks). Срок, как и прежде, — runningTTL от первого
// блока: он ограничивает, сколько один обмен может копить блоков.
type block1State struct {
	blocks      map[int][]byte
	totalBlocks int
	// assembled — собранное сообщение: блоки, повторённые после сборки, не собирают тело
	// заново, а доходят до claim и отбрасываются там.
	assembled *CoAPMessage
	expires   int64
}

var blockStates = newBlockTable(time.Minute)

func newBlockTable(sweepEvery time.Duration) *blockTable {
	t := &blockTable{}
	for i := range t.shards {
		t.shards[i].m = make(map[exKey]*block1State)
	}
	go t.sweepLoop(sweepEvery)
	return t
}

func (t *blockTable) shard(k exKey) *blockShard {
	return &t.shards[maphash.Comparable(keySeed, k)%tableShards]
}

// get возвращает состояние приёма обмена k, создавая его при первом блоке.
func (t *blockTable) get(k exKey) *block1State {
	sh := t.shard(k)
	now := monoNow()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if st, ok := sh.m[k]; ok && st.expires > now {
		return st
	}
	st := &block1State{
		blocks:      make(map[int][]byte),
		totalBlocks: -1,
		expires:     now + int64(runningTTL),
	}
	sh.m[k] = st
	return st
}

func (t *blockTable) delete(k exKey) {
	sh := t.shard(k)
	sh.mu.Lock()
	delete(sh.m, k)
	sh.mu.Unlock()
}

func (t *blockTable) sweepLoop(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for range ticker.C {
		now := monoNow()
		for i := range t.shards {
			sh := &t.shards[i]
			sh.mu.Lock()
			for k, st := range sh.m {
				if st.expires <= now {
					delete(sh.m, k)
				}
			}
			sh.mu.Unlock()
		}
	}
}

// proxyTable — обратные маршруты прокси сервера: ответ, пришедший с адреса назначения с
// токеном запроса, уходит тому, кто запрос прислал. Запись живёт ttl с последнего
// использования. Раньше это был go-cache со строковым ключом: поиск на каждое входящее
// сообщение собирал строку адреса (~5 аллокаций), а у каждого сервера своя горутина раз
// в секунду обходила всю таблицу под общим замком. Теперь просроченное вычищает одна
// горутина на процесс раз в proxySweepEvery; таблицы она держит слабыми ссылками и
// сервер, который больше никому не нужен, не удерживает (у ghost серверов тысячи).
type proxyTable struct {
	ttl    int64
	count  atomic.Int64
	shards [proxyShards]proxyShard
}

const (
	proxyShards     = 16
	proxySweepEvery = 10 * time.Second
)

type proxyShard struct {
	mu sync.Mutex
	m  map[exKey]proxyEntry
	_  [48]byte
}

type proxyEntry struct {
	note    *proxyNote
	expires int64
}

var (
	proxyTablesMu   sync.Mutex
	proxyTables     []weak.Pointer[proxyTable]
	proxySweepStart sync.Once
)

func newProxyTable(ttl time.Duration) *proxyTable {
	t := &proxyTable{ttl: int64(ttl)}
	for i := range t.shards {
		t.shards[i].m = make(map[exKey]proxyEntry)
	}

	proxyTablesMu.Lock()
	proxyTables = append(proxyTables, weak.Make(t))
	proxyTablesMu.Unlock()
	proxySweepStart.Do(func() { go sweepProxyTables() })
	return t
}

func (t *proxyTable) shard(k exKey) *proxyShard {
	return &t.shards[maphash.Comparable(keySeed, k)%proxyShards]
}

func (t *proxyTable) get(k exKey) (*proxyNote, bool) {
	if t.count.Load() == 0 {
		return nil, false
	}
	now := monoNow()
	sh := t.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.m[k]
	if !ok {
		return nil, false
	}
	if e.expires <= now {
		delete(sh.m, k)
		t.count.Add(-1)
		return nil, false
	}
	e.expires = now + t.ttl
	sh.m[k] = e
	return e.note, true
}

func (t *proxyTable) set(k exKey, note *proxyNote) {
	sh := t.shard(k)
	now := monoNow()
	sh.mu.Lock()
	if _, ok := sh.m[k]; !ok {
		t.count.Add(1)
	}
	sh.m[k] = proxyEntry{note: note, expires: now + t.ttl}
	sh.mu.Unlock()
}

// itemCount — число маршрутов, включая просроченные, но ещё не вычищенные (как и
// ItemCount у go-cache).
func (t *proxyTable) itemCount() int {
	return int(t.count.Load())
}

func sweepProxyTables() {
	ticker := time.NewTicker(proxySweepEvery)
	defer ticker.Stop()
	for range ticker.C {
		now := monoNow()
		proxyTablesMu.Lock()
		live := proxyTables[:0]
		for _, wp := range proxyTables {
			t := wp.Value()
			if t == nil {
				continue
			}
			live = append(live, wp)
			for i := range t.shards {
				sh := &t.shards[i]
				sh.mu.Lock()
				for k, e := range sh.m {
					if e.expires <= now {
						delete(sh.m, k)
						t.count.Add(-1)
					}
				}
				sh.mu.Unlock()
			}
		}
		clear(proxyTables[len(live):])
		proxyTables = live
		proxyTablesMu.Unlock()
	}
}
