package stream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver struct {
	url   string
	calls int32
}

func (f *fakeResolver) ResolveTrack(ctx context.Context, trackID string) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.url == "" {
		return "", errors.New("нет ссылки")
	}
	return f.url, nil
}

func TestProxyServesWholeTrack(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("AUDIOBYTES"))
	}))
	defer origin.Close()

	p := NewProxy(&fakeResolver{url: origin.URL}, NewBuffer(1024), origin.Client())
	rec := httptest.NewRecorder()
	p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "AUDIOBYTES" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// Перемотка в <audio> идёт через Range — без 206 seek работать не будет.
func TestProxyHonoursRange(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("0123456789"))
	}))
	defer origin.Close()

	p := NewProxy(&fakeResolver{url: origin.URL}, NewBuffer(1024), origin.Client())
	req := httptest.NewRequest(http.MethodGet, "/stream/1", nil)
	req.Header.Set("Range", "bytes=2-4")

	rec := httptest.NewRecorder()
	p.ServeTrack(rec, req, "1")

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("code = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "234" {
		t.Fatalf("body = %q, want \"234\"", rec.Body.String())
	}
}

func TestProxyServesSecondRequestFromBuffer(t *testing.T) {
	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("AUDIO"))
	}))
	defer origin.Close()

	res := &fakeResolver{url: origin.URL}
	p := NewProxy(res, NewBuffer(1024), origin.Client())

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("обращений к источнику = %d, want 1 (второй запрос — из буфера)", got)
	}
}

// Ссылка живёт около минуты. 410 — штатная ситуация, а не ошибка.
func TestProxyRetriesOnExpiredLink(t *testing.T) {
	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.Write([]byte("AUDIO"))
	}))
	defer origin.Close()

	res := &fakeResolver{url: origin.URL}
	p := NewProxy(res, NewBuffer(1024), origin.Client())

	rec := httptest.NewRecorder()
	p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 после повторного резолва", rec.Code)
	}
	if got := atomic.LoadInt32(&res.calls); got != 2 {
		t.Fatalf("резолвов = %d, want 2", got)
	}
}

func TestProxyReportsResolveFailure(t *testing.T) {
	p := NewProxy(&fakeResolver{}, NewBuffer(1024), http.DefaultClient)
	rec := httptest.NewRecorder()
	p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "не удалось") {
		t.Fatalf("тело не объясняет ошибку: %q", rec.Body.String())
	}
}

// LimitReader без запаса в один байт молча обрежет трек и вернёт nil-ошибку —
// прокси отдал бы усечённое аудио с кодом 200. Проверяем, что отказ честный.
func TestProxyRejectsOversizedTrack(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("0123456789")) // 10 байт
	}))
	defer origin.Close()

	p := NewProxy(&fakeResolver{url: origin.URL}, NewBuffer(1024), origin.Client())
	p.maxBytes = 4 // предел в несколько байт, чтобы не гонять мегабайты в тесте

	rec := httptest.NewRecorder()
	p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (усечённый трек не должен уйти с кодом 200)", rec.Code)
	}
	if rec.Body.String() == "0123456789" {
		t.Fatalf("тело содержит полный трек при коде ошибки")
	}
	if _, ok := p.buf.Get("1"); ok {
		t.Fatalf("трек, превысивший предел, не должен попасть в буфер")
	}
}

// Второй запрос, пришедший раньше, чем первый успел заполнить буфер
// (типичный сценарий: <audio> шлёт Range-запрос на перемотку в первые
// секунды воспроизведения), не должен породить второе обращение к источнику.
func TestProxyCollapsesConcurrentRequests(t *testing.T) {
	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte("AUDIOBYTES"))
	}))
	defer origin.Close()

	p := NewProxy(&fakeResolver{url: origin.URL}, NewBuffer(1024), origin.Client())

	const n = 5
	var wg sync.WaitGroup
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")
			bodies[i] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	for i, b := range bodies {
		if b != "AUDIOBYTES" {
			t.Fatalf("body[%d] = %q, want AUDIOBYTES", i, b)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("обращений к источнику = %d, want 1 (запросы должны схлопнуться)", got)
	}
}

// *url.Error от p.http.Do включает полный URL запроса — вместе с подписанной
// ссылкой на хранилище. Клиенту такие подробности уходить не должны.
func TestProxyDoesNotLeakUpstreamDetails(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	link := origin.URL + "/sign/DEADBEEF"
	origin.Close() // сервер недоступен — Do() вернёт *url.Error с полным URL

	p := NewProxy(&fakeResolver{url: link}, NewBuffer(1024), http.DefaultClient)
	rec := httptest.NewRecorder()
	p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "DEADBEEF") || strings.Contains(body, origin.URL) {
		t.Fatalf("тело ответа раскрывает подробности источника: %q", body)
	}
	if !strings.Contains(body, "не удалось") {
		t.Fatalf("тело не объясняет ошибку: %q", body)
	}
}

// panicOnceResolver паникует при каждом вызове. Первый вызов сперва
// сигнализирует entered и блокируется на release — это даёт тесту
// возможность гарантированно подписать «ожидающего» на pending-запись
// лидера до того, как резолвер запаникует.
type panicOnceResolver struct {
	entered chan struct{}
	release chan struct{}
	calls   int32
}

func (r *panicOnceResolver) ResolveTrack(ctx context.Context, trackID string) (string, error) {
	if atomic.AddInt32(&r.calls, 1) == 1 {
		close(r.entered)
		<-r.release
	}
	panic("паника резолвера")
}

// Паника внутри загрузки (например, при разборе XML резолвера) не должна
// ронять демон и не должна оставить запись в pending навсегда: без
// recover+delete+close все последующие запросы этого трека зависли бы на
// <-pf.done, который уже некому закрыть.
func TestProxyRecoversFromLeaderPanic(t *testing.T) {
	res := &panicOnceResolver{entered: make(chan struct{}), release: make(chan struct{})}
	p := NewProxy(res, NewBuffer(1024), http.DefaultClient)

	leaderDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")
		leaderDone <- rec
	}()

	select {
	case <-res.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("лидер не вошёл в резолвер")
	}

	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")
		waiterDone <- rec
	}()

	time.Sleep(20 * time.Millisecond) // дать ожидающему подписаться на pending
	close(res.release)                // резолвер паникует

	checks := []struct {
		name string
		ch   chan *httptest.ResponseRecorder
	}{
		{"лидер", leaderDone},
		{"ожидающий", waiterDone},
	}
	for _, c := range checks {
		select {
		case rec := <-c.ch:
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("%s: code = %d, want 502 (паника лидера должна обернуться в ошибку)", c.name, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Fatalf("%s: пустое тело — паника не должна превращаться в пустой успех", c.name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: запрос завис после паники резолвера — запись в pending не была снята", c.name)
		}
	}

	// Повторный запрос того же трека обязан снова попытаться загрузиться,
	// а не зависнуть на утёкшей записи pending.
	rec := httptest.NewRecorder()
	retryDone := make(chan struct{})
	go func() {
		p.ServeTrack(rec, httptest.NewRequest(http.MethodGet, "/stream/1", nil), "1")
		close(retryDone)
	}()
	select {
	case <-retryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("повторный запрос трека завис — запись в pending осталась навсегда")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("повторный запрос: code = %d, want 502", rec.Code)
	}
	if got := atomic.LoadInt32(&res.calls); got != 2 {
		t.Fatalf("резолвер вызван %d раз, want 2 (повторный запрос должен попытаться заново)", got)
	}
}

// Между промахом мимо буфера в ServeTrack (без лока) и захватом p.mu внутри
// fetchOnce лидер мог успеть отдать результат, положить его в буфер и снять
// свою запись из pending. fetchOnce обязан перепроверить буфер под тем же
// мьютексом — иначе он не найдёт запись в pending и станет новым лидером,
// хотя трек уже лежит в буфере.
func TestProxyFetchOnceRechecksBufferUnderLock(t *testing.T) {
	res := &fakeResolver{} // без url — вызов ResolveTrack вернул бы ошибку
	buf := NewBuffer(1024)
	buf.Put("1", []byte("CACHED"))
	p := NewProxy(res, buf, http.DefaultClient)

	data, err := p.fetchOnce(context.Background(), "1")
	if err != nil {
		t.Fatalf("err = %v, want nil (трек уже в буфере)", err)
	}
	if string(data) != "CACHED" {
		t.Fatalf("data = %q, want %q", data, "CACHED")
	}
	if got := atomic.LoadInt32(&res.calls); got != 0 {
		t.Fatalf("резолвер вызван %d раз, want 0 — трек должен браться из буфера, а не качаться заново", got)
	}
}

// Лидер с уже отменённым контекстом не должен утаскивать за собой
// ожидающих: типичный сценарий — <audio> открывает поток, тут же обрывает
// первый запрос и переоткрывает его с Range, пока первая загрузка ещё идёт.
func TestProxyLeaderCancellationDoesNotAffectWaiters(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Write([]byte("AUDIOBYTES"))
	}))
	defer origin.Close()

	p := NewProxy(&fakeResolver{url: origin.URL}, NewBuffer(1024), origin.Client())

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	cancelLeader() // контекст лидера уже отменён к моменту запроса

	leaderReq := httptest.NewRequest(http.MethodGet, "/stream/1", nil).WithContext(leaderCtx)
	leaderDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		p.ServeTrack(rec, leaderReq, "1")
		leaderDone <- rec
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("лидер (с уже отменённым контекстом) не дошёл до источника — контекст не был отвязан")
	}

	waiterReq := httptest.NewRequest(http.MethodGet, "/stream/1", nil) // живой контекст
	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		p.ServeTrack(rec, waiterReq, "1")
		waiterDone <- rec
	}()

	time.Sleep(20 * time.Millisecond) // дать ожидающему подписаться на pending
	close(release)

	select {
	case rec := <-waiterDone:
		if rec.Code != http.StatusOK || rec.Body.String() != "AUDIOBYTES" {
			t.Fatalf("ожидающий: code=%d body=%q, want 200/\"AUDIOBYTES\" (отменённый контекст лидера не должен на него влиять)", rec.Code, rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ожидающий завис вместо получения данных")
	}
	<-leaderDone
}
