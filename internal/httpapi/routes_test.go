package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"music212/internal/player"
	"music212/internal/ymapi"
)

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func TestHubBroadcastReachesSubscriber(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe()
	defer cancel()

	go hub.Broadcast(player.State{Status: player.StatusPlaying})

	select {
	case got := <-ch:
		if got.Status != player.StatusPlaying {
			t.Fatalf("Status = %q", got.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("подписчик не получил состояние")
	}
}

func TestHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	hub := NewHub()
	_, cancel := hub.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			hub.Broadcast(player.State{Status: player.StatusPlaying})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast заблокировался на медленном подписчике")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe()
	cancel()

	hub.Broadcast(player.State{Status: player.StatusPlaying})
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("после отписки канал должен быть закрыт")
		}
	case <-time.After(time.Second):
		t.Fatal("канал не закрыт после отписки")
	}
}

func TestSSEEmitsInitialState(t *testing.T) {
	hub := NewHub()
	app := &App{Hub: hub, Queue: player.NewQueue()}
	mux := app.Routes()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := contextWithTimeout(req, 300*time.Millisecond)
	defer cancel()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))

	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Fatalf("SSE не содержит кадра данных: %q", body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestPlayerVolumeUpdatesState(t *testing.T) {
	app := &App{Hub: NewHub(), Queue: player.NewQueue()}
	mux := app.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/volume",
		strings.NewReader(`{"volume":0.42}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	var st player.State
	json.NewDecoder(rec.Body).Decode(&st)
	if st.Volume != 0.42 {
		t.Fatalf("Volume = %v, want 0.42", st.Volume)
	}
}

// --- дополнение: goSafe не должен ронять демон и обязан прогонять функцию
// целиком. Синхронизация — через канал, закрываемый внутри самой функции,
// без time.Sleep.

func TestGoSafeRecoversFromPanic(t *testing.T) {
	done := make(chan struct{})
	goSafe("test-panic", func() error {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goSafe не выполнил функцию")
	}
	// Если тест дошёл до этой точки — паника в fn не убила процесс.
}

func TestGoSafeReportsErrorWithoutPanicking(t *testing.T) {
	done := make(chan struct{})
	goSafe("test-error", func() error {
		defer close(done)
		return errors.New("boom")
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goSafe не выполнил функцию")
	}
}

// --- дополнение: apiError не должен пропускать в ответ детали ошибки,
// в т.ч. адрес запроса из *url.Error.

func TestApiErrorDoesNotLeakURLDetails(t *testing.T) {
	app := &App{}
	err := &url.Error{
		Op:  "Get",
		URL: "https://secret.example/path?token=ABCDEF",
		Err: errors.New("connection refused"),
	}

	rec := httptest.NewRecorder()
	app.apiError(rec, err)

	body := rec.Body.String()
	if strings.Contains(body, "secret.example") || strings.Contains(body, "ABCDEF") {
		t.Fatalf("ответ утекает детали адреса: %q", body)
	}
}

// --- дополнение: обработчики плейлистов и лайков не должны разыменовывать
// Auth.Status(), когда статус ещё не получен (UID() == 0).

func TestPlaylistAndLikeHandlersRequireUID(t *testing.T) {
	paths := []string{"/api/playlists", "/api/playlists/1", "/api/likes"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// Бэкенд подставной: он должен остаться нетронутым — запрос
			// без UID обязан быть отклонён раньше, чем дело дойдёт до
			// обращения к API. Проверка только по коду 401 недостаточна:
			// реальный сетевой вызов с uid=0 тоже мог бы вернуть 401
			// (например, от невалидного токена) и замаскировать отсутствие
			// проверки — вот почему решающий сигнал здесь именно called.
			var called atomic.Bool
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				w.Write([]byte(`{"result": []}`))
			}))
			defer backend.Close()

			authInst, _ := newTestAuth()
			app := &App{
				Auth:  authInst,
				Queue: player.NewQueue(),
				Hub:   NewHub(),
				Client: func() (*ymapi.Client, error) {
					return ymapi.NewWithBase("token", backend.URL), nil
				},
			}
			mux := app.Routes()

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401", rec.Code)
			}
			if called.Load() {
				t.Fatal("запрос к API Яндекса ушёл без проверенного UID")
			}
		})
	}
}

// --- дополнение: OriginGuard отсекает запросы с чужим Origin, но пропускает
// запросы со своим адресом и запросы совсем без Origin.

func newOriginGuardedApp() http.Handler {
	app := &App{Hub: NewHub(), Queue: player.NewQueue()}
	return OriginGuard(app.Routes())
}

func TestOriginGuardBlocksForeignOrigin(t *testing.T) {
	handler := newOriginGuardedApp()

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:12345/api/player/pause", nil)
	req.Host = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestOriginGuardAllowsSameOrigin(t *testing.T) {
	handler := newOriginGuardedApp()

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:12345/api/player/pause", nil)
	req.Host = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://127.0.0.1:12345")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}

func TestOriginGuardAllowsMissingOrigin(t *testing.T) {
	handler := newOriginGuardedApp()

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:12345/api/player/pause", nil)
	req.Host = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}
