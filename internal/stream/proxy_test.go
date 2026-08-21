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
