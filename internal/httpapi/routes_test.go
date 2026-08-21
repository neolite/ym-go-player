package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// TestHandleSSEStreamsSubsequentState — сквозной тест на тело цикла рассылки
// в HandleSSE (не только на кадр, отданный до входа в цикл, как
// TestSSEEmitsInitialState). httptest.NewRecorder тут не годится: он не
// стримит, тело доступно только после возврата обработчика, а HandleSSE не
// вернётся, пока жив контекст запроса — поэтому поднимаем настоящий
// httptest.NewServer и читаем resp.Body по мере поступления кадров.
func TestHandleSSEStreamsSubsequentState(t *testing.T) {
	hub := NewHub()
	app := &App{Hub: hub, Queue: player.NewQueue()}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("создание запроса: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("запрос SSE: %v", err)
	}
	defer resp.Body.Close()

	// Читающая горутина никогда не зовёт t.Fatalf сама (она бы вызвала
	// runtime.Goexit не в горутине теста и подвесила тест) — только шлёт
	// разобранные кадры в канал, который читает горутина теста.
	frames := make(chan player.State, 4)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var st player.State
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &st); err != nil {
				continue
			}
			select {
			case frames <- st:
			case <-ctx.Done():
				return
			}
		}
	}()

	// первый кадр — начальное состояние, отданное до входа в цикл.
	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("не дождались начального кадра SSE")
	}

	hub.Broadcast(player.State{Status: player.StatusPlaying, Volume: 0.77})

	// второй кадр обязан прийти из тела цикла рассылки — если writeFrame
	// внутри for/select убрать, этот select упрётся в таймаут.
	select {
	case st := <-frames:
		if st.Status != player.StatusPlaying || st.Volume != 0.77 {
			t.Fatalf("второй кадр = %+v, хотим Status=%q Volume=0.77", st, player.StatusPlaying)
		}
	case <-time.After(time.Second):
		t.Fatal("не дождались второго кадра SSE — тело цикла рассылки не сработало")
	}

	// Обработчик и его горутину нужно дождаться до конца теста, а не
	// оставить висеть после t.Cleanup — иначе "PASS, но грязно" всплывёт
	// на -count=20.
	cancel()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("горутина чтения SSE не завершилась после отмены контекста")
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
	// Без t.Parallel(): log.SetOutput меняет глобальное состояние пакета log.
	// Подмена нужна не только для проверки факта восстановления — без неё
	// горутина goSafe (recover() пишет в лог уже ПОСЛЕ того, как fn запаниковала
	// и её defer close(done) отработал) могла бы дожить до следующего теста и
	// дописать в чужой, уже подменённый там log.SetOutput буфер: наблюдалось
	// на практике как гонка с TestGoSafeReportsErrorWithoutPanicking. Дождавшись
	// именно записи в лог, а не только close(done), гарантируем, что горутина
	// goSafe полностью завершилась до возврата из теста.
	sw := newSyncWriter()
	prevOutput := log.Writer()
	log.SetOutput(sw)
	defer log.SetOutput(prevOutput)

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

	select {
	case <-sw.done:
	case <-time.After(time.Second):
		t.Fatal("goSafe не записала восстановление после паники в лог")
	}
}

// syncWriter — приёмник для log.SetOutput, который сигнализирует о том, что
// запись фактически произошла. Обычного канала, закрываемого внутри fn
// (как в TestGoSafeRecoversFromPanic), здесь недостаточно: goSafe вызывает
// log.Printf уже ПОСЛЕ возврата fn, так что синхронизация по завершению fn
// не гарантирует, что запись в лог уже случилась — тест читал бы буфер
// раньше записи. Синхронизация именно на факте Write снимает и эту гонку,
// и гонку доступа к bytes.Buffer из разных горутин (write — под mu, close
// сигнального канала — после записи, но всё ещё под той же mu).
type syncWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
	done chan struct{}
}

func newSyncWriter() *syncWriter {
	return &syncWriter{done: make(chan struct{})}
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	w.once.Do(func() { close(w.done) })
	return n, err
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestGoSafeReportsErrorWithoutPanicking(t *testing.T) {
	// Без t.Parallel(): log.SetOutput меняет глобальное состояние пакета log,
	// подмена не должна протечь в соседние тесты.
	sw := newSyncWriter()
	prevOutput := log.Writer()
	log.SetOutput(sw)
	defer log.SetOutput(prevOutput)

	goSafe("test-error-what", func() error {
		return errors.New("boom-error-text")
	})

	select {
	case <-sw.done:
	case <-time.After(time.Second):
		t.Fatal("goSafe не записала ошибку в лог")
	}

	logged := sw.String()
	if !strings.Contains(logged, "test-error-what") {
		t.Fatalf("лог не содержит метку what: %q", logged)
	}
	if !strings.Contains(logged, "boom-error-text") {
		t.Fatalf("лог не содержит текст ошибки: %q", logged)
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
