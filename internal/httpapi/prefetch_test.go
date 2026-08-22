package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

// fakeStreamResolver резолвит любой трек в URL тестового источника.
type fakeStreamResolver struct{ url string }

func (f fakeStreamResolver) ResolveTrack(_ context.Context, _ string) (string, error) {
	return f.url, nil
}

// newPrefetchApp собирает App с настоящими прокси и буфером поверх
// тестового источника байтов — так проверяется вся цепочка префетча,
// а не её слепок.
func newPrefetchApp(t *testing.T, tracks []player.Track) (*App, *stream.Buffer, *http.ServeMux) {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("BYTES"))
	}))
	t.Cleanup(origin.Close)

	buf := stream.NewBuffer(1 << 20)
	app := &App{
		Queue:  player.NewQueue(),
		Hub:    NewHub(),
		Buffer: buf,
		Proxy:  stream.NewProxy(fakeStreamResolver{origin.URL}, buf, origin.Client()),
		Client: func() (*ymapi.Client, error) { return ymapi.NewWithBase("t", origin.URL), nil },
	}
	if tracks != nil {
		app.Queue.Set(tracks, "tracks")
	}
	return app, buf, app.Routes()
}

// waitBuffered ждёт появления трека в буфере: префетч фоновый, поэтому
// результат приходит не в ответе обработчика, а чуть позже.
func waitBuffered(t *testing.T, buf *stream.Buffer, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := buf.Get(id); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("трек %s не появился в буфере за 2 секунды", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Постановка новой очереди (POST /api/play) обязана сразу начать подкачку
// второго трека: пока играет первый, следующий уже едет по сети.
func TestPlayPrefetchesNextTrack(t *testing.T) {
	_, buf, mux := newPrefetchApp(t, nil)

	rec := httptest.NewRecorder()
	body := `{"source":"tracks","tracks":[{"id":"a","duration":200},{"id":"b","duration":180}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/play", strings.NewReader(body))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	waitBuffered(t, buf, "b")
}

// Переход «вперёд» (POST /api/player/next) сдвигает окно префетча: после
// перехода на «b» подкачиваться обязан уже «c».
func TestNextPrefetchesUpcomingTrack(t *testing.T) {
	_, buf, mux := newPrefetchApp(t, []player.Track{
		{ID: "a", Duration: 200}, {ID: "b", Duration: 180}, {ID: "c", Duration: 170},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	waitBuffered(t, buf, "c")
}

// Клик по треку внутри очереди (queue-index) — тоже смена позиции: префетч
// обязан взять трек, следующий за выбранным.
func TestQueueIndexPrefetchesAfterChosen(t *testing.T) {
	_, buf, mux := newPrefetchApp(t, []player.Track{
		{ID: "a", Duration: 200}, {ID: "b", Duration: 180}, {ID: "c", Duration: 170},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/player/queue-index", strings.NewReader(`{"index":1}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	waitBuffered(t, buf, "c")
}

// Если следующего трека нет (текущий — последний), префетч обязан молча
// ничего не делать: не паниковать и не ходить в сеть.
func TestPrefetchNoopAtQueueEnd(t *testing.T) {
	_, buf, mux := newPrefetchApp(t, []player.Track{{ID: "a", Duration: 200}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/player/next", nil))
	// next на единственном треке — конец очереди, 200 со статусом idle.
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	select {
	case <-time.After(150 * time.Millisecond):
	}
	if got := buf.Size(); got != 0 {
		t.Fatalf("буфер не пуст (%d байт) при отсутствии следующего трека", got)
	}
}
