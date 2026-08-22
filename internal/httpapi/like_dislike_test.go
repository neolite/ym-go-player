package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"music212/internal/auth"
	"music212/internal/player"
	"music212/internal/ymapi"
)

// newLikeFakeBackend имитирует оба канала, которые задействуют оценки:
// библиотеку (POST /users/{uid}/likes/tracks/{add-multiple,remove},
// форма track-ids) и фидбек ротора (POST .../feedback, асинхронный через
// goSafe). Лайки записываются строкой "МЕТОД путь ids=..." — сигнатуры
// достаточно, чтобы различить add и remove.
func newLikeFakeBackend(t *testing.T) (srv *httptest.Server, likes chan string, feedback chan map[string]any) {
	t.Helper()
	likes = make(chan string, 4)
	feedback = make(chan map[string]any, 8)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/likes/tracks/"):
			_ = r.ParseForm()
			likes <- r.Method + " " + r.URL.Path + " ids=" + r.PostForm.Get("track-ids")
		case strings.HasSuffix(r.URL.Path, "/feedback"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			feedback <- body
		case strings.HasSuffix(r.URL.Path, "/tracks"):
			w.Write([]byte(stationTracksFixture))
			return
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	return srv, likes, feedback
}

// authedWaveApp собирает App с проверенным токеном (UID() == 7) и очередью
// волны — лайк без UID обязан быть отклонён (requireUID), поэтому тестам
// нужен настоящий проход через /api/auth/token.
func authedWaveApp(t *testing.T, srv *httptest.Server, tracks []player.Track) (*App, *http.ServeMux) {
	t.Helper()
	q := player.NewQueue()
	q.Set(tracks, "wave")
	app := &App{
		Auth:  NewAuth(auth.NewMemory(), okVerify),
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	app.batchID = "batch-1"
	mux := app.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"good"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("верификация токена: code = %d", rec.Code)
	}
	return app, mux
}

// --- этап 3: лайк и дизлайк идут в тот же канал обучения станции, что и
// скип/финиш ротора.

func TestLikeAddsToLibraryAndTeachesWave(t *testing.T) {
	srv, likes, feedback := newLikeFakeBackend(t)
	defer srv.Close()

	_, mux := authedWaveApp(t, srv, []player.Track{{Available: true, ID: "a", Duration: 200}, {Available: true, ID: "b", Duration: 180}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/a/like", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	// Лайк в библиотеку синхронен: к моменту ответа вызов уже ушёл.
	select {
	case got := <-likes:
		want := "POST /users/7/likes/tracks/add-multiple ids=a"
		if got != want {
			t.Fatalf("вызов библиотеки = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("лайк в библиотеку не ушёл")
	}

	// Фидбек ротора асинхронен и обязан нести позицию — ротору важно,
	// на какой секунде трек оценили.
	select {
	case body := <-feedback:
		if body["type"] != "like" || body["trackId"] != "a" {
			t.Fatalf("кадр фидбека = %v, want like/a", body)
		}
		if _, ok := body["totalPlayedSeconds"]; !ok {
			t.Fatalf("like без totalPlayedSeconds: %v", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("лайк не ушёл в обучение волны")
	}
}

func TestDislikeTeachesWaveAndSkips(t *testing.T) {
	srv, likes, feedback := newLikeFakeBackend(t)
	defer srv.Close()

	app, mux := authedWaveApp(t, srv, []player.Track{{Available: true, ID: "a", Duration: 200}, {Available: true, ID: "b", Duration: 180}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/a/dislike", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	// Три кадра: dislike по «a», skip по «a» (дизлайк — усиленный скип),
	// trackStarted по «b» (новому текущему).
	got := collectFeedbackTypes(t, feedback, 3)
	if got["dislike"] != "a" {
		t.Fatalf("dislike trackId = %q, want a; кадры = %v", got["dislike"], got)
	}
	if got["skip"] != "a" {
		t.Fatalf("skip trackId = %q, want a; кадры = %v", got["skip"], got)
	}
	if got["trackStarted"] != "b" {
		t.Fatalf("trackStarted trackId = %q, want b; кадры = %v", got["trackStarted"], got)
	}
	if cur := app.Queue.Current(); cur == nil || cur.ID != "b" {
		t.Fatalf("после дизлайка текущий = %+v, want b", cur)
	}

	// Дизлайк не трогает библиотеку: это оценка станции, а не снятие лайка.
	select {
	case got := <-likes:
		t.Fatalf("дизлайк не должен ходить в библиотеку, ушло: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUnlikeCallsRemove(t *testing.T) {
	srv, likes, _ := newLikeFakeBackend(t)
	defer srv.Close()

	_, mux := authedWaveApp(t, srv, []player.Track{{Available: true, ID: "a", Duration: 200}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/tracks/a/like", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	select {
	case got := <-likes:
		// На стороне Яндекса снятие — POST на remove (без «-multiple»:
		// такого пути нет, боевой сервис отвечает 404). DELETE — это
		// контракт НАШЕГО эндпоинта по спеке.
		want := "POST /users/7/likes/tracks/remove ids=a"
		if got != want {
			t.Fatalf("вызов библиотеки = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("снятие лайка не ушло")
	}
}

// Лайк вне волны учит только библиотеку: станции, которой можно было бы
// адресовать событие, у источника "tracks" нет.
func TestLikeOutsideWaveDoesNotTouchRotor(t *testing.T) {
	srv, likes, feedback := newLikeFakeBackend(t)
	defer srv.Close()

	q := player.NewQueue()
	q.Set([]player.Track{{Available: true, ID: "a", Duration: 200}}, "tracks")
	plain := &App{
		Auth:  NewAuth(auth.NewMemory(), okVerify),
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	mux := plain.Routes()
	rec0 := httptest.NewRecorder()
	req0 := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"good"}`))
	req0.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec0, req0)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/a/like", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	select {
	case <-likes:
	case <-time.After(time.Second):
		t.Fatal("лайк в библиотеку не ушёл")
	}
	select {
	case body := <-feedback:
		t.Fatalf("лайк вне волны не должен учить ротор, ушло: %v", body)
	case <-time.After(300 * time.Millisecond):
	}
}

// Лайк без проверенного статуса аккаунта обязан быть отклонён до обращения
// к API: track-ids без uid не отправить.
func TestLikeRequiresUID(t *testing.T) {
	srv, likes, _ := newLikeFakeBackend(t)
	defer srv.Close()

	q := player.NewQueue()
	q.Set([]player.Track{{Available: true, ID: "a"}}, "wave")
	app := &App{
		Auth:  NewAuth(auth.NewMemory(), okVerify), // токен не верифицирован
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tracks/a/like", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	select {
	case got := <-likes:
		t.Fatalf("лайк ушёл в API без UID: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}
