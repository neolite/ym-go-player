package httpapi

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"music212/internal/player"
	"music212/internal/ymapi"
)

// markerWriter — приёмник для log.SetOutput, сигнализирующий не о первой
// записи вообще (как syncWriter в routes_test.go), а о первой записи,
// содержащей заданную подстроку. Нужен здесь, потому что за один прогон
// retryRefillWave пишет в лог дважды с разным смыслом: сразу после первой
// неудачной синхронной попытки (refillWave) и, отдельно, после исчерпания
// всех фоновых повторов (retryRefillWave) — тест должен дождаться именно
// второй записи, не перепутав её с первой.
type markerWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	once   sync.Once
	done   chan struct{}
}

func newMarkerWriter(marker string) *markerWriter {
	return &markerWriter{marker: marker, done: make(chan struct{})}
}

func (w *markerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(string(p), w.marker) {
		w.once.Do(func() { close(w.done) })
	}
	return n, err
}

func withShortRefillBackoff(t *testing.T) {
	t.Helper()
	prev := refillWaveBackoff
	refillWaveBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { refillWaveBackoff = prev })
}

// --- находка 5: отказ подкачки батча ротора не должен быть молчаливым.

func TestRefillWaveFirstFailureSetsWarningSynchronously(t *testing.T) {
	withShortRefillBackoff(t)

	// Бэкенд всегда отвечает 500 — фоновые повторы тоже провалятся и
	// исчерпаются. Дожидаемся именно этого момента (через маркер в логе)
	// перед возвратом из теста: иначе горутина retryRefillWave переживает
	// тест и конкурентно читает refillWaveBackoff, пока t.Cleanup
	// СЛЕДУЮЩЕГО теста уже пишет в ту же переменную — гонка, пойманная
	// -race на практике при первом прогоне этого файла.
	sw := newMarkerWriter("повтор подкачки батча волны")
	prevOutput := log.Writer()
	log.SetOutput(sw)
	defer log.SetOutput(prevOutput)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	q := player.NewQueue()
	q.Set([]player.Track{{ID: "a"}, {ID: "b"}}, "wave") // Remaining()==1 < 2 → триггерит refill
	app := &App{
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}

	app.refillWave(context.Background())

	// Первая попытка синхронна — к моменту возврата из refillWave
	// предупреждение обязано уже стоять, не дожидаясь фоновых повторов.
	app.mu.RLock()
	warn := app.errText
	app.mu.RUnlock()
	if warn == "" {
		t.Fatal("после неудачной первой попытки State.Error должен быть непустым")
	}

	select {
	case <-sw.done:
	case <-time.After(3 * time.Second):
		t.Fatal("не дождались завершения фоновых повторов")
	}
}

func TestRefillWaveRetrySucceedsAndClearsWarning(t *testing.T) {
	withShortRefillBackoff(t)

	var attempt atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempt.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(stationTracksFixture))
	}))
	defer srv.Close()

	q := player.NewQueue()
	q.Set([]player.Track{{ID: "a"}, {ID: "b"}}, "wave")
	hub := NewHub()
	app := &App{
		Queue: q,
		Hub:   hub,
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}

	ch, cancel := hub.Subscribe()
	defer cancel()

	app.refillWave(context.Background())

	// Кадры публикуются: сначала предупреждение (Error != ""), затем, после
	// успешного повтора, кадр с Error == "". Синхронизация — через реальные
	// кадры Hub, а не через опрос состояния таймером.
	sawWarning := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case st := <-ch:
			if st.Error != "" {
				sawWarning = true
				continue
			}
			if sawWarning {
				goto cleared
			}
		case <-deadline:
			t.Fatalf("не дождались снятия предупреждения (sawWarning=%v)", sawWarning)
		}
	}
cleared:
	if !sawWarning {
		t.Fatal("не увидели предупреждение после первой неудачной попытки")
	}
	tracks, _, _ := q.Snapshot()
	if len(tracks) != 3 {
		t.Fatalf("после успешного повтора в очередь должен добавиться батч, len(tracks) = %d, want 3", len(tracks))
	}
}

func TestRefillWaveExhaustsRetriesLeavesWarning(t *testing.T) {
	withShortRefillBackoff(t)

	sw := newMarkerWriter("повтор подкачки батча волны")
	prevOutput := log.Writer()
	log.SetOutput(sw)
	defer log.SetOutput(prevOutput)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	q := player.NewQueue()
	q.Set([]player.Track{{ID: "a"}, {ID: "b"}}, "wave")
	app := &App{
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}

	app.refillWave(context.Background())

	select {
	case <-sw.done:
	case <-time.After(3 * time.Second):
		t.Fatal("не дождались лога об исчерпанных повторах")
	}

	app.mu.RLock()
	warn := app.errText
	app.mu.RUnlock()
	if warn == "" {
		t.Fatal("после исчерпания повторов предупреждение должно остаться в State.Error")
	}
	tracks, _, _ := app.Queue.Snapshot()
	if len(tracks) != 2 {
		t.Fatalf("очередь не должна была получить новый батч, len(tracks) = %d, want 2", len(tracks))
	}
}

// clearWarning не должен затирать чужую, более позднюю ошибку: если пока
// шли фоновые повторы кто-то поставил через setStatus настоящий сбой
// воспроизведения, успешный (запоздалый) повтор не должен его стереть.
func TestClearWarningDoesNotClobberUnrelatedError(t *testing.T) {
	app := &App{Hub: NewHub(), Queue: player.NewQueue()}
	app.setWarning("предупреждение ротора")
	app.setStatus(player.StatusError, "настоящая ошибка воспроизведения")

	app.clearWarning("предупреждение ротора")

	app.mu.RLock()
	got := app.errText
	app.mu.RUnlock()
	if got != "настоящая ошибка воспроизведения" {
		t.Fatalf("errText = %q, хотим сохранённую настоящую ошибку", got)
	}
}
