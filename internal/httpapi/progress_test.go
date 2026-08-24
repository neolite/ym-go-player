package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music212/internal/player"
)

// Гонка: фронтенд шлёт позицию тиком раз в 5 секунд (web/src/app.ts). Если
// тик "старого" трека приходит после ручного переключения (queue-index/
// next/prev), которое уже обнулило позицию нового трека, он не обязан её
// переписывать — применяется только тик, чей trackId совпадает с текущим
// треком очереди.
func TestProgressIgnoresStaleTickForPreviousTrack(t *testing.T) {
	app, _, mux := newPrefetchApp(t, []player.Track{
		{Available: true, ID: "a", Duration: 200}, {Available: true, ID: "b", Duration: 180},
	})

	// "a" отыграл 120 секунд.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/progress",
		strings.NewReader(`{"position":120,"trackId":"a"}`)))

	// Ручное переключение на "b" — позиция сброшена в 0.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/queue-index",
		strings.NewReader(`{"index":1}`)))

	// Запоздалый тик от "a" не должен переписать позицию "b".
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/progress",
		strings.NewReader(`{"position":120,"trackId":"a"}`)))

	app.mu.RLock()
	pos := app.position
	app.mu.RUnlock()
	if pos != 0 {
		t.Fatalf("position = %v после запоздалого тика прошлого трека, want 0", pos)
	}

	// Тик с trackId текущего трека по-прежнему применяется как обычно.
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/player/progress",
		strings.NewReader(`{"position":15,"trackId":"b"}`)))

	app.mu.RLock()
	pos = app.position
	app.mu.RUnlock()
	if pos != 15 {
		t.Fatalf("position = %v после тика текущего трека, want 15", pos)
	}
}
