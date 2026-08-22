package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music212/internal/player"
	"music212/internal/ymapi"
)

// Спека §10: «трек недоступен в регионе — скип со сноской в UI, очередь
// продолжается». Пропуск делает сервер (skipUnavailable), не доводя до
// ошибки <audio> на фронтенде.

// deadClient — клиент с нерабочим адресом для обработчиков, которым
// объект клиента нужен по контракту (handlePlay зовёт a.client), но
// до сети дело не доходит: источник "tracks" не ходит в API.
func deadClient() (*ymapi.Client, error) { return ymapi.NewWithBase("t", "http://127.0.0.1:1"), nil }

// playSnapshot — минимальная проекция кадра состояния для этих тестов.
type playSnapshot struct {
	Status string `json:"status"`
	Track  *struct {
		ID string `json:"id"`
	} `json:"track"`
	Error string `json:"error"`
}

// postPlayer шлёт запрос и разбирает кадр состояния из ответа.
func postPlayer(t *testing.T, mux *http.ServeMux, method, path, body string) playSnapshot {
	t.Helper()
	rec := httptest.NewRecorder()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, rdr))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s: code = %d, want 200; body = %s", method, path, rec.Code, rec.Body.String())
	}
	var st playSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("разбор кадра: %v; body = %s", err, rec.Body.String())
	}
	return st
}

// Переход «вперёд» на недоступный трек обязан проскочить его к ближайшему
// доступному и оставить сноску в State.Error.
func TestNextSkipsUnavailable(t *testing.T) {
	q := player.NewQueue()
	q.Set([]player.Track{
		{ID: "a", Title: "Первая", Duration: 200, Available: true},
		{ID: "b", Title: "Закрытая", Duration: 180, Available: false},
		{ID: "c", Title: "Третья", Duration: 170, Available: true},
	}, "tracks")
	app := &App{Queue: q, Hub: NewHub()}
	mux := app.Routes()

	st := postPlayer(t, mux, http.MethodPost, "/api/player/next", "")
	if st.Track == nil || st.Track.ID != "c" {
		t.Fatalf("текущий после next = %+v, want c (b недоступен)", st.Track)
	}
	if !strings.Contains(st.Error, "Закрытая") {
		t.Fatalf("сноска не называет пропущенный трек: %q", st.Error)
	}
	if cur := app.Queue.Current(); cur == nil || cur.ID != "c" {
		t.Fatalf("Queue.Current = %+v, want c", cur)
	}
}

// Постановка очереди, начинающейся с недоступного трека, обязана сразу
// встать на первый доступный — не доводя до ошибки <audio>.
func TestPlaySkipsUnavailableAtStart(t *testing.T) {
	app := &App{Queue: player.NewQueue(), Hub: NewHub(), Client: deadClient}
	mux := app.Routes()

	body := `{"source":"tracks","tracks":[
		{"id":"x","title":"Недоступная","duration":100,"available":false},
		{"id":"y","title":"Доступная","duration":100,"available":true}]}`
	st := postPlayer(t, mux, http.MethodPost, "/api/play", body)
	if st.Track == nil || st.Track.ID != "y" {
		t.Fatalf("первый играющий = %+v, want y", st.Track)
	}
	if st.Error == "" {
		t.Fatal("ожидалась сноска о пропущенном треке")
	}
}

// Очередь из одних недоступных — честный idle с объяснением, а не молчание.
func TestPlayAllUnavailable(t *testing.T) {
	app := &App{Queue: player.NewQueue(), Hub: NewHub(), Client: deadClient}
	mux := app.Routes()

	body := `{"source":"tracks","tracks":[
		{"id":"x","title":"Раз","duration":100,"available":false},
		{"id":"y","title":"Два","duration":100,"available":false}]}`
	st := postPlayer(t, mux, http.MethodPost, "/api/play", body)
	if st.Status != "idle" {
		t.Fatalf("status = %q, want idle", st.Status)
	}
	if st.Track != nil {
		t.Fatalf("track = %+v, want null", st.Track)
	}
	if !strings.Contains(st.Error, "2 трека") {
		t.Fatalf("сноска = %q, want «2 трека»", st.Error)
	}
}

// Хвост очереди из недоступных при next — idle + сноска, очередь не
// зацикливается и не падает.
func TestNextExhaustsUnavailableTail(t *testing.T) {
	q := player.NewQueue()
	q.Set([]player.Track{
		{ID: "a", Title: "Первая", Duration: 200, Available: true},
		{ID: "b", Title: "Закрытая", Duration: 180, Available: false},
	}, "tracks")
	app := &App{Queue: q, Hub: NewHub()}
	mux := app.Routes()

	st := postPlayer(t, mux, http.MethodPost, "/api/player/next", "")
	if st.Status != "idle" {
		t.Fatalf("status = %q, want idle", st.Status)
	}
	if !strings.Contains(st.Error, "Закрытая") {
		t.Fatalf("сноска = %q, want упоминание «Закрытая»", st.Error)
	}
}

// Клик по недоступному треку в очереди ведёт на ближайший доступный следом.
func TestQueueIndexSkipsUnavailable(t *testing.T) {
	q := player.NewQueue()
	q.Set([]player.Track{
		{ID: "a", Title: "Первая", Duration: 200, Available: true},
		{ID: "b", Title: "Закрытая", Duration: 180, Available: false},
		{ID: "c", Title: "Третья", Duration: 170, Available: true},
	}, "tracks")
	app := &App{Queue: q, Hub: NewHub()}
	mux := app.Routes()

	st := postPlayer(t, mux, http.MethodPost, "/api/player/queue-index", `{"index":1}`)
	if st.Track == nil || st.Track.ID != "c" {
		t.Fatalf("текущий после клика по недоступному = %+v, want c", st.Track)
	}
	if st.Error == "" {
		t.Fatal("ожидалась сноска о пропуске")
	}
}

// Сноска о пропуске живёт до следующего перехода и снимается им самим
// (setStatus перезаписывает errText) — не требует отдельной уборки.
func TestSkipWarningClearsOnNextTransition(t *testing.T) {
	q := player.NewQueue()
	q.Set([]player.Track{
		{ID: "a", Title: "Первая", Duration: 200, Available: true},
		{ID: "b", Title: "Закрытая", Duration: 180, Available: false},
		{ID: "c", Title: "Третья", Duration: 170, Available: true},
		{ID: "d", Title: "Четвёртая", Duration: 160, Available: true},
	}, "tracks")
	app := &App{Queue: q, Hub: NewHub()}
	mux := app.Routes()

	st := postPlayer(t, mux, http.MethodPost, "/api/player/next", "")
	if st.Error == "" {
		t.Fatal("после пропуска сноски нет")
	}
	st = postPlayer(t, mux, http.MethodPost, "/api/player/next", "")
	if st.Track == nil || st.Track.ID != "d" {
		t.Fatalf("текущий = %+v, want d", st.Track)
	}
	if st.Error != "" {
		t.Fatalf("сноска пережила следующий переход: %q", st.Error)
	}
}
