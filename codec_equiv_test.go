package coalago

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// Новые Serialize и deserialize обязаны совпадать с прежними (legacy_codec_test.go):
// байты на проводе и разобранные сообщения — один в один.

var equivOptionCodes = []OptionCode{
	OptionIfMatch, OptionURIHost, OptionEtag, OptionIfNoneMatch, OptionObserve, OptionURIPort,
	OptionLocationPath, OptionURIPath, OptionContentFormat, OptionMaxAge, OptionURIQuery,
	OptionAccept, OptionLocationQuery, OptionBlock2, OptionBlock1, OptionSize2, OptionProxyURI,
	OptionProxyScheme, OptionSize1, OptionURIScheme, OptionHandshakeType, OptionSessionNotFound,
	OptionSessionExpired, OptionSelectiveRepeatWindowSize, OptionСoapsUri, OptionProxySecurityID,
	OptionChecksum, 1000, 2001, 65000,
}

func randString(r *rand.Rand) string {
	lengths := []int{0, 1, 5, 12, 13, 14, 100, 268, 269, 270, 400}
	b := make([]byte, lengths[r.IntN(len(lengths))])
	for i := range b {
		b[i] = byte(r.IntN(256))
	}
	return string(b)
}

func randOptionValue(r *rand.Rand) interface{} {
	ints := []uint64{0, 1, 12, 255, 256, 300, 65535, 65536, 1 << 20, 1<<32 - 1}
	v := ints[r.IntN(len(ints))]
	switch r.IntN(12) {
	case 0:
		return randString(r)
	case 1:
		return []byte(randString(r))
	case 2:
		return MediaType(v)
	case 3:
		return byte(v)
	case 4:
		return int(v)
	case 5:
		return int32(v)
	case 6:
		return uint(v)
	case 7:
		return uint32(v)
	case 8:
		return uint16(v) // не поддерживается valueToBytes: пустое значение
	case 9:
		return nil
	case 10:
		return -int(v)
	default:
		return randString(r)
	}
}

func randMessage(r *rand.Rand, maxOptions int) *CoAPMessage {
	m := &CoAPMessage{
		MessageID: uint16(r.IntN(65536)),
		Type:      CoapType(r.IntN(4)),
		Code:      CoapCode(r.IntN(256)),
	}
	if n := r.IntN(10); n > 0 {
		m.Token = make([]byte, n-1)
		for i := range m.Token {
			m.Token[i] = byte(r.IntN(256))
		}
	}
	for range r.IntN(maxOptions + 1) {
		m.Options = append(m.Options, NewOption(equivOptionCodes[r.IntN(len(equivOptionCodes))], randOptionValue(r)))
	}
	switch r.IntN(6) {
	case 0:
		m.Payload = NewBytesPayload([]byte(randString(r)))
	case 1:
		m.Payload = NewStringPayload(randString(r))
	case 2:
		m.Payload = NewEmptyPayload()
	case 3:
		m.Payload = NewJSONPayload(map[string]any{"s": randString(r), "n": r.IntN(1000)})
	case 4:
		m.Payload = nil
	default:
		m.Payload = NewBytesPayload(nil)
	}
	return m
}

// cloneDeep копирует сообщение вместе с опциями: Serialize сортирует опции на месте и
// может заменить URIScheme, прогоны не должны видеть изменений друг друга.
func cloneDeep(m *CoAPMessage) *CoAPMessage {
	c := *m
	c.Options = nil
	for _, o := range m.Options {
		c.Options = append(c.Options, &CoAPMessageOption{Code: o.Code, Value: o.Value})
	}
	return &c
}

func TestSerializeMatchesLegacy(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 200000 {
		m := randMessage(r, 12)
		legacyMsg, newMsg := cloneDeep(m), cloneDeep(m)
		want, errWant := legacySerialize(legacyMsg)
		got, errGot := Serialize(newMsg)
		if !errors.Is(errGot, errWant) || !bytes.Equal(got, want) {
			t.Fatalf("case %d: Serialize = %x, %v; legacy = %x, %v", i, got, errGot, want, errWant)
		}
		// Мутации сообщения (сортировка, нормализация URIScheme) — тоже как раньше.
		if !reflect.DeepEqual(optionsOf(legacyMsg), optionsOf(newMsg)) {
			t.Fatalf("case %d: options after Serialize differ: %v vs legacy %v", i, optionsOf(newMsg), optionsOf(legacyMsg))
		}
	}
}

// Больше 12 опций прежний sort.Sort (pdqsort с доработанным Swap) мог переставить
// одинаковые опции; новая сортировка стабильна. Проверяется, что порядок сегментов пути и
// query сохраняется, а длина на проводе та же, что у прежней реализации.
func TestSerializeManyOptionsKeepsRepeatableOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	// Повторяемые строковые опции и по одной числовой: иначе сообщение не пройдёт
	// проверку повторов критичных опций при разборе.
	stringCodes := []OptionCode{OptionURIPath, OptionURIQuery, OptionEtag, OptionLocationPath, OptionLocationQuery}
	intCodes := []OptionCode{OptionContentFormat, OptionObserve, OptionBlock1, OptionBlock2, OptionSize1, OptionAccept}
	differs, legacyUnsorted, legacyBrokenPath := 0, 0, 0
	for i := range 20000 {
		m := NewCoAPMessage(CON, POST)
		var path, query []string
		for _, code := range intCodes {
			if r.IntN(2) == 0 {
				m.Options = append(m.Options, NewOption(code, r.IntN(1<<20)))
			}
		}
		r.Shuffle(len(m.Options), func(a, b int) { m.Options[a], m.Options[b] = m.Options[b], m.Options[a] })
		for range 13 + r.IntN(30) {
			code := stringCodes[r.IntN(len(stringCodes))]
			v := fmt.Sprintf("s%d", r.IntN(1000))
			m.Options = append(m.Options, NewOption(code, v))
			switch code {
			case OptionURIPath:
				path = append(path, v)
			case OptionURIQuery:
				query = append(query, v)
			}
		}
		data, err := Serialize(cloneDeep(m))
		if err != nil {
			t.Fatal(err)
		}
		legacyMsg := cloneDeep(m)
		legacyData, _ := legacySerialize(legacyMsg)
		if !sortedByCode(legacyMsg.Options) {
			legacyUnsorted++ // прежняя сортировка оставила опции не по порядку
		} else if len(data) != len(legacyData) {
			t.Fatalf("case %d: length %d, legacy %d", i, len(data), len(legacyData))
		}
		if !bytes.Equal(data, legacyData) {
			differs++
		}
		if lm, err := Deserialize(legacyData); err != nil || !reflect.DeepEqual(lm.GetOptionsAsString(OptionURIPath), path) {
			legacyBrokenPath++
		}
		got, err := Deserialize(data)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		var gotPath, gotQuery []string
		for _, o := range got.Options {
			switch o.Code {
			case OptionURIPath:
				gotPath = append(gotPath, o.StringValue())
			case OptionURIQuery:
				gotQuery = append(gotQuery, o.StringValue())
			}
		}
		if !reflect.DeepEqual(gotPath, path) || !reflect.DeepEqual(gotQuery, query) {
			t.Fatalf("case %d: path %q query %q, want %q %q", i, gotPath, gotQuery, path, query)
		}
	}
	t.Logf("of 20000 messages with >12 options: %d serialize differently from the old code; the old sort left %d unsorted and broke the path in %d", differs, legacyUnsorted, legacyBrokenPath)
}

func sortedByCode(opts []*CoAPMessageOption) bool {
	for i := 1; i < len(opts); i++ {
		if opts[i].Code < opts[i-1].Code {
			return false
		}
	}
	return true
}

type optionView struct {
	Code  OptionCode
	Value interface{}
}

func optionsOf(m *CoAPMessage) []optionView {
	var out []optionView
	for _, o := range m.Options {
		out = append(out, optionView{o.Code, o.Value})
	}
	return out
}

// messageView — всё, что видно у разобранного сообщения.
type messageView struct {
	MessageID uint16
	Type      CoapType
	Code      CoapCode
	Token     []byte
	Options   []optionView
	HasBody   bool
	Body      []byte
}

func viewOf(m *CoAPMessage) *messageView {
	if m == nil {
		return nil
	}
	v := &messageView{MessageID: m.MessageID, Type: m.Type, Code: m.Code, Token: m.Token, Options: optionsOf(m)}
	if len(v.Token) == 0 {
		v.Token = nil
	}
	if m.Payload != nil {
		v.HasBody, v.Body = true, m.Payload.Bytes()
		if len(v.Body) == 0 {
			v.Body = nil
		}
	}
	return v
}

func assertSameDeserialize(t *testing.T, data []byte) {
	t.Helper()
	orig := append([]byte(nil), data...)
	want, errWant := legacyDeserialize(append([]byte(nil), data...))
	got, errGot := deserialize(data)
	if !errors.Is(errGot, errWant) && fmt.Sprint(errGot) != fmt.Sprint(errWant) {
		t.Fatalf("deserialize(%x) error %v, legacy %v", data, errGot, errWant)
	}
	if !reflect.DeepEqual(viewOf(got), viewOf(want)) {
		t.Fatalf("deserialize(%x) = %+v, legacy %+v", data, viewOf(got), viewOf(want))
	}
	if got == nil || errGot != nil {
		return
	}
	// Сообщение не ссылается на буфер: затираем буфер — сообщение не меняется.
	for i := range data {
		data[i] ^= 0xff
	}
	if !reflect.DeepEqual(viewOf(got), viewOf(want)) {
		t.Fatalf("deserialize(%x): message changed after the input buffer was overwritten", orig)
	}
}

func TestDeserializeMatchesLegacy(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range 100000 {
		data, err := Serialize(randMessage(r, 12))
		if err != nil {
			t.Fatal(err)
		}
		assertSameDeserialize(t, data)

		// Испорченные датаграммы: обрезка и случайные байты — те же ошибки и те же
		// частично разобранные сообщения.
		if len(data) > 0 {
			cut := append([]byte(nil), data[:r.IntN(len(data))]...)
			assertSameDeserialize(t, cut)
			flipped := append([]byte(nil), data...)
			for range 1 + r.IntN(4) {
				flipped[r.IntN(len(flipped))] = byte(r.IntN(256))
			}
			assertSameDeserialize(t, flipped)
		}
	}
}

func FuzzDeserializeMatchesLegacy(f *testing.F) {
	r := rand.New(rand.NewPCG(7, 8))
	for range 64 {
		data, _ := Serialize(randMessage(r, 12))
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		assertSameDeserialize(t, data)
	})
}

func TestGetURIQueryMatchesSplitN(t *testing.T) {
	m := NewCoAPMessage(CON, GET)
	for _, q := range []string{"cid=abc", "a=b=c", "=x", "noeq", "", "k=", "a=b"} {
		m.AddOption(OptionURIQuery, q)
	}
	m.AddOption(OptionURIQuery, 42) // нестроковое значение
	for _, key := range []string{"cid", "a", "", "noeq", "k", "a=b", "missing"} {
		want := ""
		for _, v := range m.GetURIQueryArray() {
			if kv := splitN2(v); len(kv) == 2 && kv[0] == key {
				want = kv[1]
				break
			}
		}
		if got := m.GetURIQuery(key); got != want {
			t.Fatalf("GetURIQuery(%q) = %q, want %q", key, got, want)
		}
	}
}

func splitN2(v string) []string {
	for i := 0; i < len(v); i++ {
		if v[i] == '=' {
			return []string{v[:i], v[i+1:]}
		}
	}
	return []string{v}
}

func TestGetURIMatchesLegacyFormula(t *testing.T) {
	cases := [][]string{
		{},
		{"=x"},
		{"a/b", "", " x "},
		{"=x", "a=b"},
		{"special key=value/with spaces&symbols=%"},
		{"a=1", "b", "c=2"},
	}
	for _, queries := range cases {
		for _, scheme := range []int{COAP_SCHEME, COAPS_SCHEME} {
			m := NewCoAPMessage(CON, GET)
			m.AddOption(OptionURIScheme, scheme)
			m.SetURIPath("/sensor/data")
			for _, q := range queries {
				m.AddOption(OptionURIQuery, q)
			}
			legacyPath := "/" + strings.Join(m.GetOptionsAsString(OptionURIPath), "/")
			if got := m.GetURIPath(); got != legacyPath {
				t.Fatalf("GetURIPath = %q, want %q", got, legacyPath)
			}
			legacy := m.GetSchemeString() + "://" + "127.0.0.1:5683" + legacyPath
			if q := m.GetURIQueryString(); len(q) > 0 {
				legacy += "?" + q
			}
			if got := m.GetURI("127.0.0.1:5683"); got != legacy {
				t.Fatalf("GetURI = %q, want %q", got, legacy)
			}
		}
	}
}
