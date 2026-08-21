package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"music212/internal/player"
	"music212/internal/ymapi"
)

// stationTracksFixture — минимальный ответ ротора на GET .../tracks,
// достаточный, чтобы StationTracks() распарсил batchId и один трек.
const stationTracksFixture = `{"result":{"batchId":"batch-fresh","sequence":[
  {"track":{"id":"w1","title":"Волна","available":true,"durationMs":90000,
   "artists":[{"name":"Икс"}],"albums":[{"title":"Игрек"}]}}
]}}`

// newRotorFakeBackend имитирует все каналы, которыми пользуется демон для
// волны: GET /rotor/station/{id}/tracks (подкачка батча), POST
// /rotor/station/{id}/feedback (JSON) и POST /play-audio (форма). Каждый
// принятый запрос фидбека уходит в свой канал — тесты читают именно
// оттуда, а не поллингом/сном, потому что оба канала фидбека асинхронны
// (goSafe).
func newRotorFakeBackend(t *testing.T) (srv *httptest.Server, feedback chan map[string]any, stats chan url.Values) {
	t.Helper()
	feedback = make(chan map[string]any, 8)
	stats = make(chan url.Values, 8)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tracks"):
			w.Write([]byte(stationTracksFixture))
			return
		case strings.HasSuffix(r.URL.Path, "/feedback"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			feedback <- body
		case r.URL.Path == "/play-audio":
			_ = r.ParseForm()
			stats <- r.PostForm
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	return srv, feedback, stats
}

// collectFeedbackTypes читает ровно n кадров фидбека ротора и индексирует их
// по полю "type" -> "trackId". Падает по таймауту, а не зависает, если
// демон недосчитался вызовов.
func collectFeedbackTypes(t *testing.T, ch chan map[string]any, n int) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i := 0; i < n; i++ {
		select {
		case body := <-ch:
			typ, _ := body["type"].(string)
			trackID, _ := body["trackId"].(string)
			out[typ] = trackID
		case <-time.After(2 * time.Second):
			t.Fatalf("не дождались %d кадров фидбека ротора (получили %d): %v", n, i, out)
		}
	}
	return out
}

func waveApp(srv *httptest.Server, tracks []player.Track) *App {
	q := player.NewQueue()
	q.Set(tracks, "wave")
	app := &App{
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	app.batchID = "batch-1"
	return app
}

// --- находка 1: /api/player/next обязан различать скип и естественное
// завершение трека, а не всегда слать trackFinished.

func TestHandleNextWithoutBodySendsSkip(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a", Duration: 200}, {ID: "b", Duration: 180}})
	mux := app.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	// Ожидаем два кадра: skip по треку "a" (тому, что скипнули) и
	// trackStarted по треку "b" (новому текущему).
	got := collectFeedbackTypes(t, feedback, 2)
	if got["skip"] != "a" {
		t.Fatalf("skip trackId = %q, want a; кадры = %v", got["skip"], got)
	}
	if _, ok := got["trackFinished"]; ok {
		t.Fatalf("без тела запроса не должно быть trackFinished, кадры = %v", got)
	}
	if got["trackStarted"] != "b" {
		t.Fatalf("trackStarted trackId = %q, want b; кадры = %v", got["trackStarted"], got)
	}
}

func TestHandleNextWithReasonFinishedSendsTrackFinished(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a", Duration: 200}, {ID: "b", Duration: 180}})
	mux := app.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/player/next", strings.NewReader(`{"reason":"finished"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	got := collectFeedbackTypes(t, feedback, 2)
	if got["trackFinished"] != "a" {
		t.Fatalf("trackFinished trackId = %q, want a; кадры = %v", got["trackFinished"], got)
	}
	if _, ok := got["skip"]; ok {
		t.Fatalf("с reason:finished не должно быть skip, кадры = %v", got)
	}
}

// TestHandleNextSendsTrackStartedForEveryWaveTrack ловит регрессию "trackStarted
// только для первого трека": два последовательных /api/player/next обязаны
// прислать trackStarted для КАЖДОГО нового текущего трека.
func TestHandleNextSendsTrackStartedForEveryWaveTrack(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	mux := app.Routes()

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	first := collectFeedbackTypes(t, feedback, 2)
	if first["trackStarted"] != "b" {
		t.Fatalf("после первого next trackStarted = %q, want b; кадры = %v", first["trackStarted"], first)
	}

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	second := collectFeedbackTypes(t, feedback, 2)
	if second["trackStarted"] != "c" {
		t.Fatalf("после второго next trackStarted = %q, want c; кадры = %v", second["trackStarted"], second)
	}
}

func TestHandlePlaySendsTrackStartedForFirstWaveTrack(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	q := player.NewQueue()
	app := &App{
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	mux := app.Routes()

	body := `{"source":"wave"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/play", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	// Ожидаем два кадра: radioStarted (постановка волны) и trackStarted
	// по первому треку батча ("w1") — именно последний был находкой 1
	// (событие не уходило вовсе).
	got := collectFeedbackTypes(t, feedback, 2)
	if _, ok := got["radioStarted"]; !ok {
		t.Fatalf("нет radioStarted, кадры = %v", got)
	}
	if got["trackStarted"] != "w1" {
		t.Fatalf("trackStarted trackId = %q, want w1; кадры = %v", got["trackStarted"], got)
	}
}

func TestHandlePrevSendsTrackStartedWhenMoved(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a"}, {ID: "b"}})
	// встаём на второй трек, чтобы Prev() было куда двигаться
	app.Queue.Next()
	mux := app.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/prev", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	got := collectFeedbackTypes(t, feedback, 1)
	if got["trackStarted"] != "a" {
		t.Fatalf("trackStarted trackId = %q, want a; кадры = %v", got["trackStarted"], got)
	}
}

// Auth может быть nil в App (см. тесты SSE/OriginGuard) — обращение к
// Auth.UID() внутри фидбека не должно паниковать демон целиком.
func TestHandleNextDoesNotPanicWithoutAuth(t *testing.T) {
	srv, _, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a"}, {ID: "b"}})
	app.Auth = nil
	mux := app.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}

// --- находка 4: сценарий "двойной скип". Пользователь слушает A четыре
// минуты, жмёт "вперёд" (скип A, позиция 240 — легитимна для A), через
// секунду жмёт ещё раз (скип B). B не звучал вовсе — оба канала фидбека
// для B обязаны получить totalPlayedSeconds/total-played-seconds == 0,
// а не унаследованные 240 от A.
func TestDoubleNextResetsPositionForNewTrack(t *testing.T) {
	srv, feedback, stats := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{
		{ID: "a", Duration: 240},
		{ID: "b", Duration: 200},
		{ID: "c", Duration: 180},
	})
	mux := app.Routes()

	// Симулируем "A уже отыграл четыре минуты" — обычно это делает
	// handleProgress тиком раз в 5 секунд.
	app.mu.Lock()
	app.position = 240
	app.mu.Unlock()

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	first := collectFeedbackTypes(t, feedback, 2) // skip(a) + trackStarted(b)
	if first["skip"] != "a" {
		t.Fatalf("первый skip trackId = %q, want a; кадры = %v", first["skip"], first)
	}
	firstStats := readStats(t, stats)
	if got := firstStats.Get("total-played-seconds"); got != "240.00" {
		t.Fatalf("total-played-seconds для a = %q, want 240.00", got)
	}
	if got := firstStats.Get("track-id"); got != "a" {
		t.Fatalf("track-id первой статистики = %q, want a", got)
	}

	// Второй "вперёд" почти сразу — B не успел проиграть ни секунды.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	second := collectFeedbackTypes(t, feedback, 2) // skip(b) + trackStarted(c)
	if second["skip"] != "b" {
		t.Fatalf("второй skip trackId = %q, want b; кадры = %v", second["skip"], second)
	}
	secondStats := readStats(t, stats)
	if got := secondStats.Get("track-id"); got != "b" {
		t.Fatalf("track-id второй статистики = %q, want b", got)
	}
	if got := secondStats.Get("total-played-seconds"); got != "0.00" {
		t.Fatalf("total-played-seconds для b = %q, want 0.00 (находка 4: унаследованная позиция от a)", got)
	}
}

// readStats читает одно значение из канала статистики /play-audio с
// таймаутом, а не блокируется навечно, если демон недосчитался вызова.
func readStats(t *testing.T, ch chan url.Values) url.Values {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("не дождались вызова /play-audio")
		return nil
	}
}

// --- находка 2: клик по треку в очереди не должен навсегда выключать
// докачку батчей и фидбек волны.

func TestHandleQueueIndexMovesWithoutChangingSource(t *testing.T) {
	srv, feedback, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	mux := app.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/player/queue-index", strings.NewReader(`{"index":2}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var st player.State
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if st.Source != "wave" {
		t.Fatalf("Source = %q, want wave — клик по треку не должен выключать волну", st.Source)
	}
	if st.QueueIndex != 2 {
		t.Fatalf("QueueIndex = %d, want 2", st.QueueIndex)
	}
	if st.Track == nil || st.Track.ID != "c" {
		t.Fatalf("Track = %+v, want id=c", st.Track)
	}

	// trackStarted обязан уйти для нового текущего трека, как и после next/prev.
	got := collectFeedbackTypes(t, feedback, 1)
	if got["trackStarted"] != "c" {
		t.Fatalf("trackStarted trackId = %q, want c; кадры = %v", got["trackStarted"], got)
	}
}

func TestHandleQueueIndexOutOfRangeReturns400(t *testing.T) {
	srv, _, _ := newRotorFakeBackend(t)
	defer srv.Close()

	app := waveApp(srv, []player.Track{{ID: "a"}, {ID: "b"}})
	mux := app.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/player/queue-index", strings.NewReader(`{"index":9}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if got := app.Queue.Current(); got == nil || got.ID != "a" {
		t.Fatalf("после отказа текущий трек не должен меняться, Current = %v", got)
	}
}
