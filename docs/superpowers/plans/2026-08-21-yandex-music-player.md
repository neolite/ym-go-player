# Легковесный плеер Яндекс Музыки — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Локальный демон на Go с веб-интерфейсом, который играет «Мою волну» и личные плейлисты Яндекс Музыки без Electron, промо-блоков и лишнего UI.

**Architecture:** Один Go-бинарь держит токен, ходит в неофициальный API Яндекса, подписывает ссылки и проксирует аудио на `127.0.0.1`; фронтенд — вшитая через `//go:embed` страница, где декодированием занимается `<audio>` браузера. Логика разделена так, что `player` не делает сетевых вызовов, а `ymapi` ничего не знает о плеере.

**Tech Stack:** Go 1.26, стандартная библиотека (`net/http`, `crypto/md5`, `crypto/hmac`, `encoding/xml`), `zalando/go-keyring` для системного хранилища токена, vanilla TypeScript + esbuild через `npx` для фронтенда.

**Spec:** [`docs/superpowers/specs/2026-08-21-yandex-music-lightweight-player-design.md`](../specs/2026-08-21-yandex-music-lightweight-player-design.md)

## Global Constraints

- **Go 1.26+**, имя модуля `music212`.
- **Минимум зависимостей.** Единственная внешняя Go-зависимость — `github.com/zalando/go-keyring`. Всё остальное — стандартная библиотека. Не добавлять веб-фреймворки, роутеры, ORM, логгеры.
- **Фронтенд без фреймворков.** Vanilla TypeScript. Не добавлять React, Vue, Svelte.
- **Слушать только `127.0.0.1`.** Никогда не биндиться на `0.0.0.0`.
- **Токен не покидает процесс демона.** Он не должен попадать ни в JSON-ответы API, ни в логи, ни во фронтенд.
- **Не реализовывать выгрузку каталога в постоянные файлы** и **не реализовывать обход технической защиты контента.** Буфер §5.3 спеки — транзитный, привязан к очереди, стирается при выходе. Если высокое качество отдаётся под защитой, работа по этому пути прекращается (Task 14).
- **Никаких промо-элементов в UI.** Ни баннеров, ни подборок, ни рекламных мест — это прямое требование заказчика.
- **TDD.** Каждая задача начинается с падающего теста и заканчивается коммитом.
- Комментарии в коде — на русском, как и вся проектная документация.

## Отклонение от спеки

**§6 спеки описывает захват токена через redirect implicit-флоу — это нереализуемо.** Токен возвращается во фрагменте URL (`#access_token=…`), который браузер не отправляет на сервер, а `redirect_uri` привязан к принадлежащему Яндексу `client_id`, который мы не контролируем. Task 11 реализует **ручную вставку токена**; OAuth Device Flow вынесен за пределы v1.

## Порядок выполнения и параллелизм

Задачи внутри одной волны независимы и могут выполняться параллельно.

| Волна | Задачи | Зависит от |
|---|---|---|
| 0 | Task 1 | — |
| 1 | Task 2, Task 3, Task 4 | 1 |
| 2 | Task 5, Task 7 | 1 |
| 3 | Task 6, Task 9, Task 10 | 2, 5 |
| 4 | Task 8, Task 11 | 6, 7, 3 |
| 5 | Task 12 | 4, 8, 9, 10, 11 |
| 6 | Task 13 | 12 |
| 7 | Task 14 | 13 |

**Task 6 — критическая точка.** Она замыкает шагающий скелет (токен → метаданные → подписанная ссылка → байты). Если она не проходит на живом API, дальнейшие волны бессмысленны.

## Структура файлов

```
music212/
├── go.mod
├── cmd/musicd/main.go              точка входа, флаги, сборка зависимостей
├── internal/
│   ├── ymapi/
│   │   ├── sign.go        подписи ссылок              Task 2
│   │   ├── client.go      HTTP-транспорт, заголовки   Task 5
│   │   ├── account.go     account/status              Task 5
│   │   ├── types.go       DTO треков и плейлистов     Task 5
│   │   ├── tracks.go      метаданные, download-info   Task 6
│   │   ├── search.go      поиск                       Task 9
│   │   ├── playlists.go   плейлисты, «Мне нравится»   Task 9
│   │   ├── rotor.go       «Моя волна»                 Task 10
│   │   └── feedback.go    play-audio, фидбек ротора   Task 10
│   ├── player/
│   │   ├── state.go       PlayerState                 Task 4
│   │   └── queue.go       автомат очереди             Task 4
│   ├── stream/
│   │   ├── buffer.go      транзитный буфер            Task 7
│   │   └── proxy.go       Range-прокси                Task 8
│   ├── auth/
│   │   └── store.go       хранилище токена            Task 3
│   └── httpapi/
│       ├── server.go      сборка роутера              Task 1, 12
│       ├── routes.go      обработчики                 Task 12
│       ├── auth.go        роуты авторизации           Task 11
│       └── sse.go         поток состояния             Task 12
└── web/
    ├── index.html                                     Task 13
    ├── src/app.ts                                     Task 13
    └── dist/                собирается esbuild, embed
```

---

### Task 1: Каркас проекта и HTTP-сервер

**Files:**
- Create: `go.mod`, `cmd/musicd/main.go`, `internal/httpapi/server.go`, `internal/httpapi/server_test.go`, `.gitignore`, `Makefile`

**Interfaces:**
- Consumes: ничего
- Produces: `httpapi.New(mux *http.ServeMux) *Server`, `(*Server).Addr() string`, `(*Server).Start() error`, `(*Server).Shutdown(ctx context.Context) error`. Сервер сам выбирает свободный порт на `127.0.0.1`, если переданный занят.

- [ ] **Step 1: Инициализировать модуль и репозиторий**

```bash
cd /Users/rafkat/Apps/dias/music-212
git init
go mod init music212
mkdir -p cmd/musicd internal/httpapi internal/ymapi internal/player internal/stream internal/auth web/src
printf 'web/dist/\nmusicd\n.DS_Store\n' > .gitignore
```

- [ ] **Step 2: Написать падающий тест**

Create `internal/httpapi/server_test.go`:

```go
package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestServerServesHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := New(mux)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerBindsLoopbackOnly(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if got := srv.Addr(); len(got) < 10 || got[:10] != "127.0.0.1:" {
		t.Fatalf("Addr = %q, want 127.0.0.1:<port>", got)
	}
	_ = time.Second
}
```

- [ ] **Step 3: Запустить тест и убедиться, что падает**

Run: `go test ./internal/httpapi/ -run TestServer -v`
Expected: FAIL — `undefined: New`

- [ ] **Step 4: Реализовать сервер**

Create `internal/httpapi/server.go`:

```go
// Package httpapi отдаёт локальный HTTP-интерфейс демона.
package httpapi

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Server оборачивает http.Server и слушает только петлевой интерфейс.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// New создаёт сервер с заданным роутером. Порт выбирается при Start.
func New(mux *http.ServeMux) *Server {
	return &Server{
		http: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start занимает свободный порт на 127.0.0.1 и начинает обслуживание.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	go s.http.Serve(ln)
	return nil
}

// Addr возвращает фактический адрес вида 127.0.0.1:54321.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown корректно останавливает сервер.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}
```

- [ ] **Step 5: Написать точку входа**

Create `cmd/musicd/main.go`:

```go
// Command musicd — локальный демон плеера Яндекс Музыки.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"music212/internal/httpapi"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := httpapi.New(mux)
	if err := srv.Start(); err != nil {
		log.Fatalf("не удалось запустить сервер: %v", err)
	}
	fmt.Printf("плеер слушает http://%s\n", srv.Addr())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("ошибка остановки: %v", err)
	}
}
```

- [ ] **Step 6: Добавить Makefile**

Create `Makefile`:

```make
.PHONY: test build run
test:
	go test ./... -count=1
build:
	go build -o musicd ./cmd/musicd
run: build
	./musicd
```

- [ ] **Step 7: Запустить тесты**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 8: Коммит**

```bash
git add go.mod cmd internal .gitignore Makefile
git commit -m "feat: каркас проекта и локальный HTTP-сервер"
```

---

### Task 2: Подписи ссылок

**Files:**
- Create: `internal/ymapi/sign.go`, `internal/ymapi/sign_test.go`

**Interfaces:**
- Consumes: ничего (чистые функции стандартной библиотеки)
- Produces:
  - `ymapi.SignMP3(path, s string) string` — hex-строка MD5
  - `ymapi.DirectLinkMP3(host, path, ts, s string) string` — готовый URL
  - `ymapi.SignFileInfo(key string, parts ...string) string` — base64 без padding

- [ ] **Step 1: Написать падающий тест с золотыми векторами**

Create `internal/ymapi/sign_test.go`:

```go
package ymapi

import "testing"

// Вектор посчитан независимо: md5("XGRlBW9FXlekgbPrRHuSiA" + "abc/def.mp3" + "somesalt").
func TestSignMP3(t *testing.T) {
	got := SignMP3("/abc/def.mp3", "somesalt")
	const want = "cb7ecaa6654cd1134af397cdbeb178e2"
	if got != want {
		t.Fatalf("SignMP3 = %q, want %q", got, want)
	}
}

// Первый символ path обязан отбрасываться — это часть протокола, а не косметика.
func TestSignMP3DropsLeadingSlash(t *testing.T) {
	withSlash := SignMP3("/abc/def.mp3", "somesalt")
	if withSlash == SignMP3("abc/def.mp3", "somesalt") {
		t.Fatal("подпись не должна совпадать: ведущий слэш обязан отбрасываться")
	}
}

func TestDirectLinkMP3(t *testing.T) {
	got := DirectLinkMP3("s1.example.net", "/abc/def.mp3", "1700000000", "somesalt")
	const want = "https://s1.example.net/get-mp3/cb7ecaa6654cd1134af397cdbeb178e2/1700000000/abc/def.mp3"
	if got != want {
		t.Fatalf("DirectLinkMP3 = %q, want %q", got, want)
	}
}

// Вектор посчитан независимо: hmac-sha256 ключом "7tvSmFbyf5hJnIHhCimDDD"
// по конкатенации "1700000000"+"12345"+"lossless"+"flac"+"raw", base64 без padding.
func TestSignFileInfo(t *testing.T) {
	got := SignFileInfo("7tvSmFbyf5hJnIHhCimDDD", "1700000000", "12345", "lossless", "flac", "raw")
	const want = "DevZeCG/M+0jdeH6+xBnQ4+IBNXCimprqilmw1mnEw8"
	if got != want {
		t.Fatalf("SignFileInfo = %q, want %q", got, want)
	}
}

func TestSignFileInfoHasNoPadding(t *testing.T) {
	got := SignFileInfo("k", "a")
	for _, c := range got {
		if c == '=' {
			t.Fatalf("подпись содержит padding: %q", got)
		}
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/ymapi/ -run TestSign -v`
Expected: FAIL — `undefined: SignMP3`

- [ ] **Step 3: Реализовать подписи**

Create `internal/ymapi/sign.go`:

```go
package ymapi

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// saltMP3 — соль старой схемы подписи прямых ссылок.
const saltMP3 = "XGRlBW9FXlekgbPrRHuSiA"

// KeyFileInfo — ключ новой схемы (get-file-info).
// ВНИМАНИЕ: не подтверждён первичным источником, проверяется в Task 14.
const KeyFileInfo = "7tvSmFbyf5hJnIHhCimDDD"

// SignMP3 считает подпись прямой ссылки по старой схеме.
// path приходит из XML download-info и всегда начинается со слэша,
// который в подпись не входит.
func SignMP3(path, s string) string {
	trimmed := strings.TrimPrefix(path, "/")
	sum := md5.Sum([]byte(saltMP3 + trimmed + s))
	return hex.EncodeToString(sum[:])
}

// DirectLinkMP3 собирает готовый URL для скачивания потока.
// Ссылка живёт около минуты, после чего отдаёт 410.
func DirectLinkMP3(host, path, ts, s string) string {
	return fmt.Sprintf("https://%s/get-mp3/%s/%s%s", host, SignMP3(path, s), ts, path)
}

// SignFileInfo считает подпись новой схемы: HMAC-SHA256 по конкатенации
// частей, base64 без padding.
func SignFileInfo(key string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strings.Join(parts, "")))
	return strings.TrimRight(base64.StdEncoding.EncodeToString(mac.Sum(nil)), "=")
}
```

- [ ] **Step 4: Запустить тесты**

Run: `go test ./internal/ymapi/ -run TestSign -v`
Expected: PASS, все пять тестов

- [ ] **Step 5: Коммит**

```bash
git add internal/ymapi/sign.go internal/ymapi/sign_test.go
git commit -m "feat(ymapi): подписи прямых ссылок, golden-тесты"
```

---

### Task 3: Хранилище токена

**Files:**
- Create: `internal/auth/store.go`, `internal/auth/store_test.go`

**Interfaces:**
- Consumes: ничего
- Produces:
  - `auth.Store` — интерфейс с `Get() (string, error)`, `Set(token string) error`, `Delete() error`
  - `auth.ErrNoToken` — сентинельная ошибка отсутствия токена
  - `auth.NewKeyring() Store` — реализация поверх системного keychain
  - `auth.NewMemory() Store` — реализация в памяти, для тестов и для `--no-keychain`

- [ ] **Step 1: Добавить зависимость**

```bash
go get github.com/zalando/go-keyring@latest
```

- [ ] **Step 2: Написать падающий тест**

Create `internal/auth/store_test.go`:

```go
package auth

import (
	"errors"
	"testing"
)

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemory()

	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Get на пустом хранилище = %v, want ErrNoToken", err)
	}
	if err := s.Set("secret-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "secret-token" {
		t.Fatalf("Get = %q, want %q", got, "secret-token")
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Get после Delete = %v, want ErrNoToken", err)
	}
}

func TestMemoryStoreRejectsEmptyToken(t *testing.T) {
	s := NewMemory()
	if err := s.Set(""); err == nil {
		t.Fatal("Set(\"\") должен возвращать ошибку")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := NewMemory()
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete на пустом хранилище должен быть безошибочным, got %v", err)
	}
}
```

- [ ] **Step 3: Запустить тест и убедиться, что падает**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — `undefined: NewMemory`

- [ ] **Step 4: Реализовать хранилище**

Create `internal/auth/store.go`:

```go
// Package auth хранит OAuth-токен Яндекса вне кода и вне репозитория.
package auth

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNoToken означает, что токен ещё не сохранён.
var ErrNoToken = errors.New("токен не сохранён")

const (
	keyringService = "music212"
	keyringUser    = "yandex-music-oauth"
)

// Store — хранилище единственного токена.
type Store interface {
	Get() (string, error)
	Set(token string) error
	Delete() error
}

type keyringStore struct{}

// NewKeyring возвращает хранилище поверх системного keychain.
func NewKeyring() Store { return keyringStore{} }

func (keyringStore) Get() (string, error) {
	v, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", ErrNoToken
	}
	return v, nil
}

func (keyringStore) Set(token string) error {
	if token == "" {
		return errors.New("пустой токен")
	}
	return keyring.Set(keyringService, keyringUser, token)
}

func (keyringStore) Delete() error {
	err := keyring.Delete(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type memoryStore struct {
	mu    sync.RWMutex
	token string
}

// NewMemory возвращает хранилище в памяти: для тестов и режима без keychain.
func NewMemory() Store { return &memoryStore{} }

func (m *memoryStore) Get() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.token == "" {
		return "", ErrNoToken
	}
	return m.token, nil
}

func (m *memoryStore) Set(token string) error {
	if token == "" {
		return errors.New("пустой токен")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = token
	return nil
}

func (m *memoryStore) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = ""
	return nil
}
```

- [ ] **Step 5: Запустить тесты**

Run: `go test ./internal/auth/ -v`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add internal/auth go.mod go.sum
git commit -m "feat(auth): хранилище токена в системном keychain"
```

---

### Task 4: Автомат очереди и состояние плеера

**Files:**
- Create: `internal/player/state.go`, `internal/player/queue.go`, `internal/player/queue_test.go`

**Interfaces:**
- Consumes: ничего (модуль намеренно не делает сетевых вызовов)
- Produces:
  - `player.Track{ID, Title, Artists []string, Album, CoverURL string, Duration int, Available bool}`
  - `player.Status` со значениями `StatusIdle`, `StatusLoading`, `StatusPlaying`, `StatusPaused`, `StatusError`
  - `player.State{Status, Track *Track, Position, Duration, Volume float64, Queue []Track, QueueIndex int, Source string, Error string}`
  - `player.NewQueue() *Queue` с методами `Set(tracks []Track, source string)`, `Append(tracks []Track)`, `Current() *Track`, `Next() bool`, `Prev() bool`, `Remaining() int`, `Snapshot() ([]Track, int, string)`

- [ ] **Step 1: Написать падающий тест**

Create `internal/player/queue_test.go`:

```go
package player

import "testing"

func tracks(ids ...string) []Track {
	out := make([]Track, 0, len(ids))
	for _, id := range ids {
		out = append(out, Track{ID: id, Available: true})
	}
	return out
}

func TestEmptyQueueHasNoCurrent(t *testing.T) {
	q := NewQueue()
	if q.Current() != nil {
		t.Fatal("Current на пустой очереди должен быть nil")
	}
	if q.Next() {
		t.Fatal("Next на пустой очереди должен вернуть false")
	}
}

func TestSetStartsAtFirstTrack(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b", "c"), "playlist")
	if got := q.Current(); got == nil || got.ID != "a" {
		t.Fatalf("Current = %v, want a", got)
	}
}

func TestNextAdvancesAndStopsAtEnd(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "playlist")

	if !q.Next() || q.Current().ID != "b" {
		t.Fatal("Next должен перейти на b")
	}
	if q.Next() {
		t.Fatal("Next на последнем треке должен вернуть false")
	}
	if q.Current().ID != "b" {
		t.Fatal("после отказа Next текущий трек не должен меняться")
	}
}

func TestPrevStopsAtStart(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "playlist")
	if q.Prev() {
		t.Fatal("Prev на первом треке должен вернуть false")
	}
	q.Next()
	if !q.Prev() || q.Current().ID != "a" {
		t.Fatal("Prev должен вернуть на a")
	}
}

// Ротор дозаливает треки на ходу — позиция при этом обязана сохраняться.
func TestAppendKeepsPosition(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "wave")
	q.Next()
	q.Append(tracks("c", "d"))

	if q.Current().ID != "b" {
		t.Fatalf("Append сдвинул позицию: Current = %s", q.Current().ID)
	}
	if got := q.Remaining(); got != 2 {
		t.Fatalf("Remaining = %d, want 2", got)
	}
}

func TestRemainingCountsTracksAfterCurrent(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b", "c"), "wave")
	if got := q.Remaining(); got != 2 {
		t.Fatalf("Remaining = %d, want 2", got)
	}
}

func TestSnapshotReturnsSource(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a"), "wave")
	_, idx, src := q.Snapshot()
	if idx != 0 || src != "wave" {
		t.Fatalf("Snapshot = (%d, %q), want (0, \"wave\")", idx, src)
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/player/ -v`
Expected: FAIL — `undefined: NewQueue`

- [ ] **Step 3: Реализовать состояние**

Create `internal/player/state.go`:

```go
// Package player хранит очередь и состояние воспроизведения.
// Модуль намеренно не делает сетевых вызовов — это делает его тестируемым без сети.
package player

// Status — стадия воспроизведения.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusLoading Status = "loading"
	StatusPlaying Status = "playing"
	StatusPaused  Status = "paused"
	StatusError   Status = "error"
)

// Track — минимум метаданных, нужный для отрисовки и воспроизведения.
type Track struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
	Album     string   `json:"album"`
	CoverURL  string   `json:"coverUrl"`
	Duration  int      `json:"duration"`
	Available bool     `json:"available"`
}

// State — единственный источник правды о воспроизведении.
// Фронтенд получает его через SSE и своего состояния не хранит.
type State struct {
	Status     Status  `json:"status"`
	Track      *Track  `json:"track"`
	Position   float64 `json:"position"`
	Duration   float64 `json:"duration"`
	Volume     float64 `json:"volume"`
	Queue      []Track `json:"queue"`
	QueueIndex int     `json:"queueIndex"`
	Source     string  `json:"source"`
	Error      string  `json:"error,omitempty"`
}
```

- [ ] **Step 4: Реализовать очередь**

Create `internal/player/queue.go`:

```go
package player

import "sync"

// Queue — позиция в списке треков. Все методы безопасны для конкурентного вызова.
type Queue struct {
	mu     sync.RWMutex
	tracks []Track
	index  int
	source string
}

// NewQueue создаёт пустую очередь.
func NewQueue() *Queue { return &Queue{} }

// Set заменяет очередь целиком и встаёт на первый трек.
func (q *Queue) Set(tracks []Track, source string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = append([]Track(nil), tracks...)
	q.index = 0
	q.source = source
}

// Append дозаливает треки в конец, не трогая текущую позицию.
// Так работает ротор: он подкачивает батчи прямо во время воспроизведения.
func (q *Queue) Append(tracks []Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = append(q.tracks, tracks...)
}

// Current возвращает текущий трек либо nil, если очередь пуста.
func (q *Queue) Current() *Track {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.index < 0 || q.index >= len(q.tracks) {
		return nil
	}
	t := q.tracks[q.index]
	return &t
}

// Next сдвигает позицию вперёд. false означает конец очереди.
func (q *Queue) Next() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.index+1 >= len(q.tracks) {
		return false
	}
	q.index++
	return true
}

// Prev сдвигает позицию назад. false означает начало очереди.
func (q *Queue) Prev() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.index <= 0 {
		return false
	}
	q.index--
	return true
}

// Remaining — сколько треков осталось после текущего.
// Ротор использует это, чтобы решить, пора ли подкачивать батч.
func (q *Queue) Remaining() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	n := len(q.tracks) - q.index - 1
	if n < 0 {
		return 0
	}
	return n
}

// Snapshot отдаёт копию очереди, позицию и источник.
func (q *Queue) Snapshot() ([]Track, int, string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return append([]Track(nil), q.tracks...), q.index, q.source
}
```

- [ ] **Step 5: Запустить тесты**

Run: `go test ./internal/player/ -v`
Expected: PASS, все семь тестов

- [ ] **Step 6: Коммит**

```bash
git add internal/player
git commit -m "feat(player): автомат очереди и состояние воспроизведения"
```

---

### Task 5: HTTP-клиент Яндекса и статус аккаунта

**Files:**
- Create: `internal/ymapi/client.go`, `internal/ymapi/types.go`, `internal/ymapi/account.go`, `internal/ymapi/account_test.go`

**Interfaces:**
- Consumes: ничего
- Produces:
  - `ymapi.New(token string) *Client` и `ymapi.NewWithBase(token, baseURL string) *Client` (второй нужен тестам, чтобы подсунуть `httptest`)
  - `(*Client).Get(ctx context.Context, path string, query url.Values, out any) error`
  - `(*Client).PostForm(ctx context.Context, path string, form url.Values, out any) error`
  - `ymapi.ErrUnauthorized`, `ymapi.ErrForbidden`
  - `ymapi.AccountStatus{UID int64, Login string, Region int, HasPlus bool}`
  - `(*Client).AccountStatus(ctx context.Context) (*AccountStatus, error)`

- [ ] **Step 1: Написать падающий тест**

Create `internal/ymapi/account_test.go`:

```go
package ymapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const accountStatusFixture = `{"result":{
  "account":{"uid":1234567,"login":"tester","region":225},
  "plus":{"hasPlus":true}
}}`

func TestAccountStatusParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/status" {
			t.Errorf("path = %q, want /account/status", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "OAuth test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(accountStatusFixture))
	}))
	defer srv.Close()

	c := NewWithBase("test-token", srv.URL)
	got, err := c.AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	if got.UID != 1234567 || got.Login != "tester" || got.Region != 225 || !got.HasPlus {
		t.Fatalf("AccountStatus = %+v", got)
	}
}

func TestUnauthorizedIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewWithBase("bad", srv.URL)
	_, err := c.AccountStatus(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestForbiddenIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewWithBase("t", srv.URL)
	_, err := c.AccountStatus(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/ymapi/ -run TestAccount -v`
Expected: FAIL — `undefined: NewWithBase`

- [ ] **Step 3: Реализовать транспорт**

Create `internal/ymapi/client.go`:

```go
package ymapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL — точка входа неофициального API.
const DefaultBaseURL = "https://api.music.yandex.net"

// userAgent имитирует официальный клиент: часть эндпоинтов на это смотрит.
const userAgent = "Yandex-Music-API"

var (
	// ErrUnauthorized — токен невалиден или истёк.
	ErrUnauthorized = errors.New("токен невалиден или истёк")
	// ErrForbidden — доступ запрещён: чаще всего нет подписки или регион.
	ErrForbidden = errors.New("доступ запрещён")
)

// Client — транспорт к API Яндекса. Токен не покидает эту структуру.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// New создаёт клиент к боевому API.
func New(token string) *Client { return NewWithBase(token, DefaultBaseURL) }

// NewWithBase позволяет подменить адрес — используется в тестах.
func NewWithBase(token, base string) *Client {
	return &Client{
		token: token,
		base:  strings.TrimRight(base, "/"),
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// HTTPClient отдаёт внутренний http.Client для загрузки потоков.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Get выполняет GET и разбирает конверт {"result": ...} в out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// PostForm выполняет POST формы и разбирает конверт в out.
func (c *Client) PostForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

// PostJSON выполняет POST с телом JSON.
func (c *Client) PostJSON(ctx context.Context, path string, query url.Values, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "OAuth "+c.token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("api вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("разбор конверта: %w", err)
	}
	if len(envelope.Result) == 0 {
		return errors.New("пустой result в ответе")
	}
	return json.Unmarshal(envelope.Result, out)
}
```

- [ ] **Step 4: Реализовать статус аккаунта**

Create `internal/ymapi/account.go`:

```go
package ymapi

import "context"

// AccountStatus — то, что нужно знать на старте: кто мы, есть ли Плюс, какой регион.
type AccountStatus struct {
	UID     int64
	Login   string
	Region  int
	HasPlus bool
}

type accountStatusResult struct {
	Account struct {
		UID    int64  `json:"uid"`
		Login  string `json:"login"`
		Region int    `json:"region"`
	} `json:"account"`
	Plus struct {
		HasPlus bool `json:"hasPlus"`
	} `json:"plus"`
}

// AccountStatus проверяет валидность токена и наличие подписки.
// Вызывается на старте: без Плюса плеер не имеет смысла.
func (c *Client) AccountStatus(ctx context.Context) (*AccountStatus, error) {
	var res accountStatusResult
	if err := c.Get(ctx, "/account/status", nil, &res); err != nil {
		return nil, err
	}
	return &AccountStatus{
		UID:     res.Account.UID,
		Login:   res.Account.Login,
		Region:  res.Account.Region,
		HasPlus: res.Plus.HasPlus,
	}, nil
}
```

- [ ] **Step 5: Создать общие DTO**

Create `internal/ymapi/types.go`:

```go
package ymapi

import (
	"strconv"
	"strings"

	"music212/internal/player"
)

// apiTrack — форма трека в ответах API. Поле ID приходит то строкой, то числом,
// поэтому разбирается как json.Number-совместимая строка.
type apiTrack struct {
	ID         any    `json:"id"`
	Title      string `json:"title"`
	Available  bool   `json:"available"`
	DurationMs int    `json:"durationMs"`
	CoverURI   string `json:"coverUri"`
	Artists    []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Albums []struct {
		Title string `json:"title"`
	} `json:"albums"`
}

// toPlayer переводит форму API во внутреннюю модель.
func (t apiTrack) toPlayer() player.Track {
	artists := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		artists = append(artists, a.Name)
	}
	album := ""
	if len(t.Albums) > 0 {
		album = t.Albums[0].Title
	}
	return player.Track{
		ID:        idString(t.ID),
		Title:     t.Title,
		Artists:   artists,
		Album:     album,
		CoverURL:  coverURL(t.CoverURI),
		Duration:  t.DurationMs / 1000,
		Available: t.Available,
	}
}

// idString нормализует идентификатор, который API отдаёт то строкой, то числом.
func idString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return ""
	}
}

// coverURL достраивает шаблон обложки до конкретного размера.
// API отдаёт путь с плейсхолдером %%, например "avatars.yandex.net/get-music-content/1/%%".
func coverURL(uri string) string {
	if uri == "" {
		return ""
	}
	return "https://" + strings.Replace(uri, "%%", "400x400", 1)
}
```

- [ ] **Step 6: Запустить тесты**

Run: `go test ./internal/ymapi/ -v`
Expected: PASS

- [ ] **Step 7: Коммит**

```bash
git add internal/ymapi
git commit -m "feat(ymapi): HTTP-транспорт, типизированные ошибки, статус аккаунта"
```

---

### Task 6: Метаданные трека и резолв прямой ссылки

> **Это критическая задача плана.** Она замыкает шагающий скелет. Строится на старой MP3/AAC-схеме, подтверждённой первичным источником, — новая схема `get-file-info` намеренно отложена до Task 14.

**Files:**
- Create: `internal/ymapi/tracks.go`, `internal/ymapi/tracks_test.go`

**Interfaces:**
- Consumes: `Client` (Task 5), `SignMP3`/`DirectLinkMP3` (Task 2), `player.Track` (Task 4)
- Produces:
  - `ymapi.DownloadVariant{Codec string, BitrateKbps int, InfoURL string, Preview bool}`
  - `(*Client).Tracks(ctx, ids []string) ([]player.Track, error)`
  - `(*Client).DownloadVariants(ctx, trackID string) ([]DownloadVariant, error)`
  - `(*Client).ResolveDirectLink(ctx context.Context, v DownloadVariant) (string, error)`
  - `ymapi.PickBest(vs []DownloadVariant) (DownloadVariant, bool)` — выбирает вариант с наибольшим битрейтом, игнорируя превью

- [ ] **Step 1: Написать падающий тест**

Create `internal/ymapi/tracks_test.go`:

```go
package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tracksFixture = `{"result":[{
  "id":"12345","title":"Тестовый трек","available":true,"durationMs":183000,
  "coverUri":"avatars.example.net/get-music-content/1/%%",
  "artists":[{"name":"Первый"},{"name":"Второй"}],
  "albums":[{"title":"Альбом"}]
}]}`

const downloadInfoFixture = `{"result":[
  {"codec":"mp3","bitrateInKbps":192,"downloadInfoUrl":"BASE/dl/192","preview":false,"direct":false},
  {"codec":"mp3","bitrateInKbps":320,"downloadInfoUrl":"BASE/dl/320","preview":false,"direct":false},
  {"codec":"aac","bitrateInKbps":64,"downloadInfoUrl":"BASE/dl/64","preview":true,"direct":false}
]}`

const downloadXMLFixture = `<?xml version="1.0" encoding="utf-8"?>
<download-info>
  <host>s1.example.net</host>
  <path>/abc/def.mp3</path>
  <ts>1700000000</ts>
  <region>225</region>
  <s>somesalt</s>
</download-info>`

func TestTracksParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).Tracks(context.Background(), []string{"12345"})
	if err != nil {
		t.Fatalf("Tracks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	tr := got[0]
	if tr.ID != "12345" || tr.Title != "Тестовый трек" || tr.Duration != 183 {
		t.Fatalf("track = %+v", tr)
	}
	if len(tr.Artists) != 2 || tr.Artists[0] != "Первый" {
		t.Fatalf("artists = %v", tr.Artists)
	}
	if tr.CoverURL != "https://avatars.example.net/get-music-content/1/400x400" {
		t.Fatalf("coverURL = %q", tr.CoverURL)
	}
}

func TestPickBestIgnoresPreview(t *testing.T) {
	vs := []DownloadVariant{
		{Codec: "mp3", BitrateKbps: 192},
		{Codec: "mp3", BitrateKbps: 320},
		{Codec: "aac", BitrateKbps: 500, Preview: true},
	}
	best, ok := PickBest(vs)
	if !ok || best.BitrateKbps != 320 {
		t.Fatalf("PickBest = %+v, ok=%v; want 320 kbps", best, ok)
	}
}

func TestPickBestOnEmpty(t *testing.T) {
	if _, ok := PickBest(nil); ok {
		t.Fatal("PickBest на пустом списке должен вернуть ok=false")
	}
}

// Ключевой тест шагающего скелета: download-info -> XML -> подписанная ссылка.
func TestResolveDirectLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(downloadXMLFixture))
	}))
	defer srv.Close()

	c := NewWithBase("t", srv.URL)
	got, err := c.ResolveDirectLink(context.Background(), DownloadVariant{InfoURL: srv.URL + "/dl/320"})
	if err != nil {
		t.Fatalf("ResolveDirectLink: %v", err)
	}
	const want = "https://s1.example.net/get-mp3/cb7ecaa6654cd1134af397cdbeb178e2/1700000000/abc/def.mp3"
	if got != want {
		t.Fatalf("ResolveDirectLink = %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/ymapi/ -run 'TestTracks|TestPickBest|TestResolve' -v`
Expected: FAIL — `undefined: DownloadVariant`

- [ ] **Step 3: Реализовать**

Create `internal/ymapi/tracks.go`:

```go
package ymapi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"music212/internal/player"
)

// DownloadVariant — один доступный вариант качества для трека.
type DownloadVariant struct {
	Codec       string
	BitrateKbps int
	InfoURL     string
	Preview     bool
}

// Tracks возвращает метаданные треков по идентификаторам.
func (c *Client) Tracks(ctx context.Context, ids []string) ([]player.Track, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	form := url.Values{"trackIds": {strings.Join(ids, ",")}}
	var res []apiTrack
	if err := c.PostForm(ctx, "/tracks", form, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res))
	for _, t := range res {
		out = append(out, t.toPlayer())
	}
	return out, nil
}

// DownloadVariants перечисляет доступные качества трека.
func (c *Client) DownloadVariants(ctx context.Context, trackID string) ([]DownloadVariant, error) {
	var res []struct {
		Codec           string `json:"codec"`
		BitrateInKbps   int    `json:"bitrateInKbps"`
		DownloadInfoURL string `json:"downloadInfoUrl"`
		Preview         bool   `json:"preview"`
	}
	if err := c.Get(ctx, "/tracks/"+trackID+"/download-info", nil, &res); err != nil {
		return nil, err
	}
	out := make([]DownloadVariant, 0, len(res))
	for _, v := range res {
		out = append(out, DownloadVariant{
			Codec:       v.Codec,
			BitrateKbps: v.BitrateInKbps,
			InfoURL:     v.DownloadInfoURL,
			Preview:     v.Preview,
		})
	}
	return out, nil
}

// PickBest выбирает вариант с наибольшим битрейтом, отбрасывая превью.
// Превью — это 30-секундные обрезки, которые приходят без подписки.
func PickBest(vs []DownloadVariant) (DownloadVariant, bool) {
	var best DownloadVariant
	found := false
	for _, v := range vs {
		if v.Preview {
			continue
		}
		if !found || v.BitrateKbps > best.BitrateKbps {
			best, found = v, true
		}
	}
	return best, found
}

// downloadInfoXML — форма XML-документа с данными для сборки прямой ссылки.
type downloadInfoXML struct {
	XMLName xml.Name `xml:"download-info"`
	Host    string   `xml:"host"`
	Path    string   `xml:"path"`
	TS      string   `xml:"ts"`
	S       string   `xml:"s"`
}

// ResolveDirectLink забирает XML по InfoURL и собирает подписанную ссылку.
// Результат живёт около минуты, поэтому вызывать нужно непосредственно перед
// чтением потока, а не заранее при построении очереди.
func (c *Client) ResolveDirectLink(ctx context.Context, v DownloadVariant) (string, error) {
	if v.InfoURL == "" {
		return "", errors.New("пустой downloadInfoUrl")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.InfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download-info вернул %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	var info downloadInfoXML
	if err := xml.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("разбор download-info XML: %w", err)
	}
	if info.Host == "" || info.Path == "" {
		return "", errors.New("download-info XML не содержит host/path")
	}
	return DirectLinkMP3(info.Host, info.Path, info.TS, info.S), nil
}
```

- [ ] **Step 4: Запустить тесты**

Run: `go test ./internal/ymapi/ -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add internal/ymapi/tracks.go internal/ymapi/tracks_test.go
git commit -m "feat(ymapi): метаданные треков и резолв подписанной ссылки"
```

---

### Task 7: Транзитный буфер воспроизведения

**Files:**
- Create: `internal/stream/buffer.go`, `internal/stream/buffer_test.go`

**Interfaces:**
- Consumes: ничего
- Produces:
  - `stream.NewBuffer(maxBytes int64) *Buffer`
  - `(*Buffer).Get(id string) ([]byte, bool)`
  - `(*Buffer).Put(id string, data []byte)`
  - `(*Buffer).Retain(ids ...string)` — удаляет всё, чего нет в списке
  - `(*Buffer).Clear()`
  - `(*Buffer).Size() int64`

> Границы из §5.3 спеки: держим текущий и один следующий трек; 256 МБ — страховочный предел, не основное правило. Буфер живёт только в памяти процесса и исчезает при выходе. На диск не пишем — это прямо исключено скоупом.

- [ ] **Step 1: Написать падающий тест**

Create `internal/stream/buffer_test.go`:

```go
package stream

import "testing"

func TestBufferRoundTrip(t *testing.T) {
	b := NewBuffer(1024)
	if _, ok := b.Get("a"); ok {
		t.Fatal("пустой буфер не должен ничего отдавать")
	}
	b.Put("a", []byte("hello"))
	got, ok := b.Get("a")
	if !ok || string(got) != "hello" {
		t.Fatalf("Get = %q, ok=%v", got, ok)
	}
}

func TestBufferEvictsOldestOverCap(t *testing.T) {
	b := NewBuffer(10)
	b.Put("a", make([]byte, 6))
	b.Put("b", make([]byte, 6))

	if _, ok := b.Get("a"); ok {
		t.Fatal("самая старая запись должна быть вытеснена")
	}
	if _, ok := b.Get("b"); !ok {
		t.Fatal("свежая запись должна остаться")
	}
	if b.Size() > 10 {
		t.Fatalf("Size = %d, превышает предел", b.Size())
	}
}

func TestBufferRejectsEntryLargerThanCap(t *testing.T) {
	b := NewBuffer(10)
	b.Put("big", make([]byte, 20))
	if _, ok := b.Get("big"); ok {
		t.Fatal("запись больше всего буфера не должна сохраняться")
	}
}

// Retain — то, как буфер следует за очередью: всё, кроме текущего
// и следующего трека, выбрасывается.
func TestRetainDropsEverythingElse(t *testing.T) {
	b := NewBuffer(1024)
	b.Put("a", []byte("1"))
	b.Put("b", []byte("2"))
	b.Put("c", []byte("3"))

	b.Retain("b", "c")

	if _, ok := b.Get("a"); ok {
		t.Fatal("a должен быть выброшен")
	}
	if _, ok := b.Get("b"); !ok {
		t.Fatal("b должен остаться")
	}
	if _, ok := b.Get("c"); !ok {
		t.Fatal("c должен остаться")
	}
}

func TestClearEmptiesBuffer(t *testing.T) {
	b := NewBuffer(1024)
	b.Put("a", []byte("1"))
	b.Clear()
	if b.Size() != 0 {
		t.Fatalf("Size после Clear = %d, want 0", b.Size())
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/stream/ -v`
Expected: FAIL — `undefined: NewBuffer`

- [ ] **Step 3: Реализовать буфер**

Create `internal/stream/buffer.go`:

```go
// Package stream проксирует аудиопоток и держит транзитный буфер.
package stream

import "sync"

// DefaultMaxBytes — страховочный предел буфера (256 МБ).
// Основное ограничение задаётся вызовом Retain: текущий и следующий трек.
const DefaultMaxBytes int64 = 256 << 20

// Buffer — транзитное хранилище байтов треков в памяти процесса.
// Не пишет на диск и не переживает завершение программы: это буфер
// воспроизведения, а не библиотека.
type Buffer struct {
	mu       sync.Mutex
	entries  map[string][]byte
	order    []string
	maxBytes int64
	size     int64
}

// NewBuffer создаёт буфер с заданным потолком в байтах.
func NewBuffer(maxBytes int64) *Buffer {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Buffer{entries: make(map[string][]byte), maxBytes: maxBytes}
}

// Get возвращает байты трека, если они есть в буфере.
func (b *Buffer) Get(id string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.entries[id]
	return data, ok
}

// Put кладёт байты трека, вытесняя самые старые записи при переполнении.
// Запись, которая одна не помещается в буфер, отбрасывается целиком.
func (b *Buffer) Put(id string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := int64(len(data))
	if n > b.maxBytes {
		return
	}
	if _, exists := b.entries[id]; exists {
		b.removeLocked(id)
	}
	for b.size+n > b.maxBytes && len(b.order) > 0 {
		b.removeLocked(b.order[0])
	}
	b.entries[id] = data
	b.order = append(b.order, id)
	b.size += n
}

// Retain оставляет только перечисленные записи. Так буфер следует за очередью.
func (b *Buffer) Retain(ids ...string) {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range append([]string(nil), b.order...) {
		if !keep[id] {
			b.removeLocked(id)
		}
	}
}

// Clear опустошает буфер. Вызывается при завершении работы.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = make(map[string][]byte)
	b.order = nil
	b.size = 0
}

// Size — текущий занятый объём в байтах.
func (b *Buffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// removeLocked удаляет запись. Вызывать только под захваченным мьютексом.
func (b *Buffer) removeLocked(id string) {
	data, ok := b.entries[id]
	if !ok {
		return
	}
	b.size -= int64(len(data))
	delete(b.entries, id)
	for i, v := range b.order {
		if v == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}
```

- [ ] **Step 4: Запустить тесты**

Run: `go test ./internal/stream/ -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add internal/stream
git commit -m "feat(stream): транзитный буфер воспроизведения"
```

---

### Task 8: Аудио-прокси с поддержкой перемотки

**Files:**
- Create: `internal/stream/proxy.go`, `internal/stream/proxy_test.go`

**Interfaces:**
- Consumes: `Buffer` (Task 7); резолвер ссылок реализуется `*ymapi.Client` (Task 6)
- Produces:
  - `stream.Resolver` — интерфейс `ResolveTrack(ctx context.Context, trackID string) (string, error)`
  - `stream.NewProxy(r Resolver, b *Buffer, hc *http.Client) *Proxy`
  - `(*Proxy).ServeTrack(w http.ResponseWriter, req *http.Request, trackID string)`

> Ключевое решение: трек целиком загружается в буфер, после чего отдаётся через `http.ServeContent`. Стандартная библиотека сама корректно обрабатывает заголовки `Range`, `If-Range` и `206 Partial Content` — писать разбор диапазонов руками не нужно и не следует. Ответ на 410 — один повторный резолв ссылки: она живёт около минуты, и истечение это штатная ситуация, а не ошибка.

- [ ] **Step 1: Написать падающий тест**

Create `internal/stream/proxy_test.go`:

```go
package stream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
	_ = io.Discard
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/stream/ -run TestProxy -v`
Expected: FAIL — `undefined: NewProxy`

- [ ] **Step 3: Реализовать прокси**

Create `internal/stream/proxy.go`:

```go
package stream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxTrackBytes ограничивает размер одного трека, который мы готовы держать
// в памяти. Полуторачасовой концертник в lossless сюда не поместится — и не должен.
const maxTrackBytes = 64 << 20

// Resolver отдаёт свежую подписанную ссылку на трек.
// Реализуется *ymapi.Client.
type Resolver interface {
	ResolveTrack(ctx context.Context, trackID string) (string, error)
}

// Proxy отдаёт аудио фронтенду, скрывая от него ссылки и токен.
type Proxy struct {
	resolver Resolver
	buf      *Buffer
	http     *http.Client
}

// NewProxy собирает прокси. hc может быть nil — тогда берётся клиент по умолчанию.
func NewProxy(r Resolver, b *Buffer, hc *http.Client) *Proxy {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Proxy{resolver: r, buf: b, http: hc}
}

// ServeTrack отдаёт трек с поддержкой перемотки.
// Загруженный трек кладётся в буфер, поэтому повторные запросы и перемотка
// не порождают новых обращений к Яндексу.
func (p *Proxy) ServeTrack(w http.ResponseWriter, req *http.Request, trackID string) {
	data, ok := p.buf.Get(trackID)
	if !ok {
		var err error
		data, err = p.fetch(req.Context(), trackID)
		if err != nil {
			http.Error(w, "не удалось получить трек: "+err.Error(), http.StatusBadGateway)
			return
		}
		p.buf.Put(trackID, data)
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent сам разбирает Range и отдаёт 206 — свой разбор диапазонов не нужен.
	http.ServeContent(w, req, trackID+".mp3", time.Time{}, bytes.NewReader(data))
}

// fetch резолвит ссылку и читает трек целиком.
// При 410 (истёкшая ссылка) делается один повторный резолв.
func (p *Proxy) fetch(ctx context.Context, trackID string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		link, err := p.resolver.ResolveTrack(ctx, trackID)
		if err != nil {
			return nil, err
		}
		data, status, err := p.download(ctx, link)
		if err == nil {
			return data, nil
		}
		if status == http.StatusGone && attempt == 0 {
			continue // ссылка протухла — берём свежую
		}
		return nil, err
	}
	return nil, fmt.Errorf("ссылка истекает быстрее, чем удаётся её использовать")
}

func (p *Proxy) download(ctx context.Context, link string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("источник вернул %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTrackBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}
```

- [ ] **Step 4: Добавить резолвер в ymapi**

Append to `internal/ymapi/tracks.go`:

```go
// ResolveTrack реализует stream.Resolver: от идентификатора трека
// до готовой подписанной ссылки одним вызовом.
func (c *Client) ResolveTrack(ctx context.Context, trackID string) (string, error) {
	variants, err := c.DownloadVariants(ctx, trackID)
	if err != nil {
		return "", err
	}
	best, ok := PickBest(variants)
	if !ok {
		return "", errors.New("нет доступных вариантов качества (проверьте подписку Плюс)")
	}
	return c.ResolveDirectLink(ctx, best)
}
```

- [ ] **Step 5: Запустить тесты**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add internal/stream internal/ymapi/tracks.go
git commit -m "feat(stream): аудио-прокси с Range и повтором при истёкшей ссылке"
```

---

### Task 9: Поиск и плейлисты

**Files:**
- Create: `internal/ymapi/search.go`, `internal/ymapi/playlists.go`, `internal/ymapi/library_test.go`

**Interfaces:**
- Consumes: `Client` (Task 5), `apiTrack` (Task 5), `player.Track` (Task 4)
- Produces:
  - `(*Client).SearchTracks(ctx context.Context, text string) ([]player.Track, error)`
  - `ymapi.PlaylistInfo{Kind int, Title string, TrackCount int, CoverURL string}`
  - `(*Client).UserPlaylists(ctx context.Context, uid int64) ([]PlaylistInfo, error)`
  - `(*Client).PlaylistTracks(ctx context.Context, uid int64, kind int) ([]player.Track, error)`
  - `(*Client).LikedTracks(ctx context.Context, uid int64) ([]player.Track, error)`

- [ ] **Step 1: Написать падающий тест**

Create `internal/ymapi/library_test.go`:

```go
package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const searchFixture = `{"result":{"tracks":{"results":[
  {"id":"1","title":"Найденный","available":true,"durationMs":60000,
   "artists":[{"name":"Артист"}],"albums":[{"title":"Альбом"}]}
]}}}`

const playlistsFixture = `{"result":[
  {"kind":1001,"title":"Мой плейлист","trackCount":12,"cover":{"uri":"cdn/%%"}}
]}`

const playlistTracksFixture = `{"result":{"tracks":[
  {"track":{"id":"7","title":"Из плейлиста","available":true,"durationMs":120000,
   "artists":[{"name":"А"}],"albums":[{"title":"Б"}]}}
]}}`

const likesFixture = `{"result":{"library":{"tracks":[{"id":"42"},{"id":"43"}]}}}`

func TestSearchTracks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("text"); got != "запрос" {
			t.Errorf("text = %q", got)
		}
		if got := r.URL.Query().Get("type"); got != "track" {
			t.Errorf("type = %q", got)
		}
		w.Write([]byte(searchFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).SearchTracks(context.Background(), "запрос")
	if err != nil {
		t.Fatalf("SearchTracks: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Найденный" {
		t.Fatalf("результат = %+v", got)
	}
}

func TestUserPlaylists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/555/playlists/list" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(playlistsFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).UserPlaylists(context.Background(), 555)
	if err != nil {
		t.Fatalf("UserPlaylists: %v", err)
	}
	if len(got) != 1 || got[0].Kind != 1001 || got[0].Title != "Мой плейлист" || got[0].TrackCount != 12 {
		t.Fatalf("плейлисты = %+v", got)
	}
}

func TestPlaylistTracksUnwrapsNesting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(playlistTracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).PlaylistTracks(context.Background(), 555, 1001)
	if err != nil {
		t.Fatalf("PlaylistTracks: %v", err)
	}
	if len(got) != 1 || got[0].ID != "7" || got[0].Title != "Из плейлиста" {
		t.Fatalf("треки = %+v", got)
	}
}

// «Мне нравится» отдаёт только идентификаторы — метаданные добираются отдельно.
func TestLikedTracksResolvesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tracks" {
			w.Write([]byte(tracksFixture))
			return
		}
		w.Write([]byte(likesFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).LikedTracks(context.Background(), 555)
	if err != nil {
		t.Fatalf("LikedTracks: %v", err)
	}
	if len(got) != 1 || got[0].ID != "12345" {
		t.Fatalf("лайки = %+v", got)
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/ymapi/ -run 'TestSearch|TestUserPlaylists|TestPlaylist|TestLiked' -v`
Expected: FAIL — `undefined: SearchTracks`

- [ ] **Step 3: Реализовать поиск**

Create `internal/ymapi/search.go`:

```go
package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// SearchTracks ищет треки по текстовому запросу.
func (c *Client) SearchTracks(ctx context.Context, text string) ([]player.Track, error) {
	q := url.Values{
		"text":              {text},
		"type":              {"track"},
		"page":              {"0"},
		"nocorrect":         {"false"},
		"playlist-in-best":  {"false"},
	}
	var res struct {
		Tracks struct {
			Results []apiTrack `json:"results"`
		} `json:"tracks"`
	}
	if err := c.Get(ctx, "/search", q, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks.Results))
	for _, t := range res.Tracks.Results {
		out = append(out, t.toPlayer())
	}
	return out, nil
}
```

- [ ] **Step 4: Реализовать плейлисты**

Create `internal/ymapi/playlists.go`:

```go
package ymapi

import (
	"context"
	"strconv"

	"music212/internal/player"
)

// PlaylistInfo — карточка плейлиста для списка в библиотеке.
type PlaylistInfo struct {
	Kind       int    `json:"kind"`
	Title      string `json:"title"`
	TrackCount int    `json:"trackCount"`
	CoverURL   string `json:"coverUrl"`
}

// UserPlaylists перечисляет плейлисты пользователя.
func (c *Client) UserPlaylists(ctx context.Context, uid int64) ([]PlaylistInfo, error) {
	var res []struct {
		Kind       int    `json:"kind"`
		Title      string `json:"title"`
		TrackCount int    `json:"trackCount"`
		Cover      struct {
			URI string `json:"uri"`
		} `json:"cover"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/playlists/list"
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	out := make([]PlaylistInfo, 0, len(res))
	for _, p := range res {
		out = append(out, PlaylistInfo{
			Kind:       p.Kind,
			Title:      p.Title,
			TrackCount: p.TrackCount,
			CoverURL:   coverURL(p.Cover.URI),
		})
	}
	return out, nil
}

// PlaylistTracks возвращает содержимое плейлиста.
// API оборачивает каждый трек в объект с полем track — разворачиваем.
func (c *Client) PlaylistTracks(ctx context.Context, uid int64, kind int) ([]player.Track, error) {
	var res struct {
		Tracks []struct {
			Track apiTrack `json:"track"`
		} `json:"tracks"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/playlists/" + strconv.Itoa(kind)
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks))
	for _, w := range res.Tracks {
		out = append(out, w.Track.toPlayer())
	}
	return out, nil
}

// LikedTracks возвращает «Мне нравится».
// Эндпоинт отдаёт только идентификаторы, поэтому метаданные добираются
// вторым запросом через /tracks.
func (c *Client) LikedTracks(ctx context.Context, uid int64) ([]player.Track, error) {
	var res struct {
		Library struct {
			Tracks []struct {
				ID any `json:"id"`
			} `json:"tracks"`
		} `json:"library"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/likes/tracks"
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Library.Tracks))
	for _, t := range res.Library.Tracks {
		if id := idString(t.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return c.Tracks(ctx, ids)
}
```

- [ ] **Step 5: Запустить тесты**

Run: `go test ./internal/ymapi/ -v`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add internal/ymapi/search.go internal/ymapi/playlists.go internal/ymapi/library_test.go
git commit -m "feat(ymapi): поиск, плейлисты и «Мне нравится»"
```

---

### Task 10: «Моя волна» и фидбек прослушивания

**Files:**
- Create: `internal/ymapi/rotor.go`, `internal/ymapi/feedback.go`, `internal/ymapi/rotor_test.go`

**Interfaces:**
- Consumes: `Client` (Task 5), `apiTrack` (Task 5), `player.Track` (Task 4)
- Produces:
  - `ymapi.WaveStationID` — константа `"user:onyourwave"`
  - `ymapi.WaveBatch{BatchID string, Tracks []player.Track}`
  - `(*Client).StationTracks(ctx context.Context, station string, lastTrackID string) (*WaveBatch, error)`
  - `(*Client).RotorFeedback(ctx context.Context, station, batchID, eventType, trackID string, playedSeconds float64) error`
  - `(*Client).PlayAudio(ctx context.Context, ev PlayEvent) error`
  - `ymapi.PlayEvent{TrackID, AlbumID, From string, PlayedSeconds, TotalSeconds float64}`

> Два независимых канала фидбека, оба обязательны (§5.4 спеки). `RotorFeedback` обучает саму волну, `PlayAudio` питает общую статистику и рекомендации аккаунта. Без них волна деградирует.

- [ ] **Step 1: Написать падающий тест**

Create `internal/ymapi/rotor_test.go`:

```go
package ymapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const stationTracksFixture = `{"result":{"batchId":"batch-42","sequence":[
  {"track":{"id":"101","title":"Волна раз","available":true,"durationMs":90000,
   "artists":[{"name":"Икс"}],"albums":[{"title":"Игрек"}]}},
  {"track":{"id":"102","title":"Волна два","available":true,"durationMs":95000,
   "artists":[{"name":"Икс"}],"albums":[{"title":"Игрек"}]}}
]}}`

func TestStationTracksParsesBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rotor/station/user:onyourwave/tracks" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("queue"); got != "99" {
			t.Errorf("queue = %q, want 99", got)
		}
		w.Write([]byte(stationTracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).StationTracks(context.Background(), WaveStationID, "99")
	if err != nil {
		t.Fatalf("StationTracks: %v", err)
	}
	if got.BatchID != "batch-42" {
		t.Fatalf("BatchID = %q", got.BatchID)
	}
	if len(got.Tracks) != 2 || got.Tracks[0].ID != "101" {
		t.Fatalf("треки = %+v", got.Tracks)
	}
}

func TestRotorFeedbackSendsEvent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if got := r.URL.Query().Get("batch-id"); got != "batch-42" {
			t.Errorf("batch-id = %q", got)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", "trackFinished", "101", 90)
	if err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if body["type"] != "trackFinished" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["trackId"] != "101" {
		t.Fatalf("trackId = %v", body["trackId"])
	}
	if body["totalPlayedSeconds"] != float64(90) {
		t.Fatalf("totalPlayedSeconds = %v", body["totalPlayedSeconds"])
	}
}

func TestPlayAudioSendsForm(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).PlayAudio(context.Background(), PlayEvent{
		TrackID: "101", AlbumID: "9", From: "wave", PlayedSeconds: 88, TotalSeconds: 90,
	})
	if err != nil {
		t.Fatalf("PlayAudio: %v", err)
	}
	if v := form["track-id"]; len(v) == 0 || v[0] != "101" {
		t.Fatalf("track-id = %v", form["track-id"])
	}
	if v := form["total-played-seconds"]; len(v) == 0 || v[0] != "88.00" {
		t.Fatalf("total-played-seconds = %v", form["total-played-seconds"])
	}
	if v := form["from"]; len(v) == 0 || v[0] != "wave" {
		t.Fatalf("from = %v", form["from"])
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/ymapi/ -run 'TestStation|TestRotor|TestPlayAudio' -v`
Expected: FAIL — `undefined: WaveStationID`

- [ ] **Step 3: Реализовать ротор**

Create `internal/ymapi/rotor.go`:

```go
package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// WaveStationID — идентификатор станции «Моя волна».
const WaveStationID = "user:onyourwave"

// WaveBatch — очередная порция треков от ротора.
// BatchID обязателен при отправке фидбека: без него волна не понимает,
// к какой выдаче относится событие.
type WaveBatch struct {
	BatchID string
	Tracks  []player.Track
}

// StationTracks запрашивает следующий батч станции.
// lastTrackID сообщает ротору, чем закончилась предыдущая порция;
// при первом обращении передаётся пустая строка.
func (c *Client) StationTracks(ctx context.Context, station, lastTrackID string) (*WaveBatch, error) {
	q := url.Values{"settings2": {"true"}}
	if lastTrackID != "" {
		q.Set("queue", lastTrackID)
	}
	var res struct {
		BatchID  string `json:"batchId"`
		Sequence []struct {
			Track apiTrack `json:"track"`
		} `json:"sequence"`
	}
	if err := c.Get(ctx, "/rotor/station/"+station+"/tracks", q, &res); err != nil {
		return nil, err
	}
	tracks := make([]player.Track, 0, len(res.Sequence))
	for _, s := range res.Sequence {
		tracks = append(tracks, s.Track.toPlayer())
	}
	return &WaveBatch{BatchID: res.BatchID, Tracks: tracks}, nil
}
```

- [ ] **Step 4: Реализовать фидбек**

Create `internal/ymapi/feedback.go`:

```go
package ymapi

import (
	"context"
	"net/url"
	"strconv"
)

// Типы событий ротора.
const (
	EventRadioStarted  = "radioStarted"
	EventTrackStarted  = "trackStarted"
	EventTrackFinished = "trackFinished"
	EventSkip          = "skip"
)

// RotorFeedback сообщает волне, что произошло с треком.
// Это канал обучения самой станции: без него выдача деградирует.
func (c *Client) RotorFeedback(ctx context.Context, station, batchID, eventType, trackID string, playedSeconds float64) error {
	body := map[string]any{
		"type":      eventType,
		"timestamp": nowUnixFloat(),
	}
	if trackID != "" {
		body["trackId"] = trackID
	}
	if eventType == EventTrackFinished || eventType == EventSkip {
		body["totalPlayedSeconds"] = playedSeconds
	}
	q := url.Values{}
	if batchID != "" {
		q.Set("batch-id", batchID)
	}
	return c.PostJSON(ctx, "/rotor/station/"+station+"/feedback", q, body, nil)
}

// PlayEvent — завершённое прослушивание для общей статистики.
type PlayEvent struct {
	TrackID       string
	AlbumID       string
	From          string
	PlayedSeconds float64
	TotalSeconds  float64
}

// PlayAudio отправляет событие прослушивания.
// Это второй, независимый от ротора канал: он питает рекомендации аккаунта
// целиком, а не только волну.
func (c *Client) PlayAudio(ctx context.Context, ev PlayEvent) error {
	form := url.Values{
		"track-id":              {ev.TrackID},
		"album-id":              {ev.AlbumID},
		"from":                  {ev.From},
		"play-id":               {""},
		"uid":                   {""},
		"timestamp":             {formatFloat(nowUnixFloat())},
		"track-length-seconds":  {formatFloat(ev.TotalSeconds)},
		"total-played-seconds":  {formatFloat(ev.PlayedSeconds)},
		"end-position-seconds":  {formatFloat(ev.PlayedSeconds)},
		"client-now":            {formatFloat(nowUnixFloat())},
	}
	return c.PostForm(ctx, "/play-audio", form, nil)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
```

- [ ] **Step 5: Добавить источник времени**

Append to `internal/ymapi/client.go`:

```go
// nowUnixFloat вынесен отдельно, чтобы тесты могли подменять время
// без обращения к системным часам.
var nowUnixFloat = func() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
```

- [ ] **Step 6: Запустить тесты**

Run: `go test ./internal/ymapi/ -v`
Expected: PASS

- [ ] **Step 7: Коммит**

```bash
git add internal/ymapi/rotor.go internal/ymapi/feedback.go internal/ymapi/rotor_test.go internal/ymapi/client.go
git commit -m "feat(ymapi): «Моя волна» и оба канала фидбека"
```

---

### Task 11: Ввод и валидация токена

> Реализует **ручную вставку токена** — см. «Отклонение от спеки» в шапке плана. Автоматический захват через redirect невозможен: токен приходит во фрагменте URL, а `redirect_uri` принадлежит Яндексу.

**Files:**
- Create: `internal/httpapi/auth.go`, `internal/httpapi/auth_test.go`

**Interfaces:**
- Consumes: `auth.Store` (Task 3), `ymapi.New`/`AccountStatus` (Task 5)
- Produces:
  - `httpapi.AuthState{Authorized bool, Login string, HasPlus bool, Region int, Message string}`
  - `httpapi.NewAuth(store auth.Store, verify VerifyFunc) *Auth`
  - `httpapi.VerifyFunc` — `func(ctx context.Context, token string) (*ymapi.AccountStatus, error)`
  - `(*Auth).Register(mux *http.ServeMux)` — вешает `GET /api/auth/status`, `POST /api/auth/token`, `POST /api/auth/logout`
  - `(*Auth).Token() (string, error)` — для остальных обработчиков
  - `httpapi.AuthorizeURL` — константа со ссылкой, которую показываем пользователю

- [ ] **Step 1: Написать падающий тест**

Create `internal/httpapi/auth_test.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music212/internal/auth"
	"music212/internal/ymapi"
)

func okVerify(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
	if token != "good" {
		return nil, ymapi.ErrUnauthorized
	}
	return &ymapi.AccountStatus{UID: 7, Login: "tester", Region: 225, HasPlus: true}, nil
}

func newTestAuth() (*Auth, *http.ServeMux) {
	a := NewAuth(auth.NewMemory(), okVerify)
	mux := http.NewServeMux()
	a.Register(mux)
	return a, mux
}

func decodeAuthState(t *testing.T, body *httptest.ResponseRecorder) AuthState {
	t.Helper()
	var st AuthState
	if err := json.NewDecoder(body.Body).Decode(&st); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	return st
}

func TestStatusUnauthorizedWithoutToken(t *testing.T) {
	_, mux := newTestAuth()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))

	st := decodeAuthState(t, rec)
	if st.Authorized {
		t.Fatal("без токена Authorized должен быть false")
	}
	if st.Message == "" {
		t.Fatal("ответ обязан объяснять, что делать дальше")
	}
}

func TestPostGoodTokenAuthorizes(t *testing.T) {
	_, mux := newTestAuth()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	st := decodeAuthState(t, rec)
	if !st.Authorized || st.Login != "tester" || !st.HasPlus {
		t.Fatalf("state = %+v", st)
	}
}

func TestPostBadTokenIsRejectedAndNotStored(t *testing.T) {
	a, mux := newTestAuth()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"bad"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if _, err := a.Token(); !errors.Is(err, auth.ErrNoToken) {
		t.Fatal("невалидный токен не должен сохраняться")
	}
}

// Токен не должен утекать во фронтенд ни при каких обстоятельствах.
func TestStatusNeverLeaksToken(t *testing.T) {
	_, mux := newTestAuth()
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))

	if strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("ответ содержит токен: %s", rec.Body.String())
	}
}

func TestLogoutClearsToken(t *testing.T) {
	a, mux := newTestAuth()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if _, err := a.Token(); !errors.Is(err, auth.ErrNoToken) {
		t.Fatal("после logout токен должен быть стёрт")
	}
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/httpapi/ -run 'TestStatus|TestPost|TestLogout' -v`
Expected: FAIL — `undefined: NewAuth`

- [ ] **Step 3: Реализовать**

Create `internal/httpapi/auth.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"music212/internal/auth"
	"music212/internal/ymapi"
)

// AuthorizeURL — страница, где пользователь получает токен.
// После входа токен виден во фрагменте адресной строки; перехватить его
// программно нельзя, поэтому пользователь копирует его вручную.
const AuthorizeURL = "https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d"

// AuthState — то, что видит фронтенд. Токена здесь нет и быть не должно.
type AuthState struct {
	Authorized bool   `json:"authorized"`
	Login      string `json:"login,omitempty"`
	HasPlus    bool   `json:"hasPlus"`
	Region     int    `json:"region,omitempty"`
	Message    string `json:"message,omitempty"`
	AuthURL    string `json:"authUrl,omitempty"`
}

// VerifyFunc проверяет токен и возвращает статус аккаунта.
type VerifyFunc func(ctx context.Context, token string) (*ymapi.AccountStatus, error)

// Auth обслуживает роуты авторизации и хранит проверенный статус.
type Auth struct {
	store  auth.Store
	verify VerifyFunc

	mu     sync.RWMutex
	status *ymapi.AccountStatus
}

// NewAuth собирает обработчик авторизации.
func NewAuth(store auth.Store, verify VerifyFunc) *Auth {
	return &Auth{store: store, verify: verify}
}

// DefaultVerify — боевая проверка токена через API.
func DefaultVerify(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
	return ymapi.New(token).AccountStatus(ctx)
}

// Register вешает роуты авторизации на роутер.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", a.handleStatus)
	mux.HandleFunc("POST /api/auth/token", a.handleSetToken)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
}

// Token отдаёт сохранённый токен остальным обработчикам.
func (a *Auth) Token() (string, error) { return a.store.Get() }

// Status возвращает последний проверенный статус аккаунта.
func (a *Auth) Status() *ymapi.AccountStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *Auth) handleStatus(w http.ResponseWriter, r *http.Request) {
	token, err := a.store.Get()
	if err != nil {
		writeJSON(w, http.StatusOK, AuthState{
			Message: "Токен не задан. Откройте ссылку, войдите и вставьте токен из адресной строки.",
			AuthURL: AuthorizeURL,
		})
		return
	}
	st, err := a.verify(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusOK, AuthState{
			Message: "Токен больше не работает — получите новый.",
			AuthURL: AuthorizeURL,
		})
		return
	}
	a.remember(st)
	writeJSON(w, http.StatusOK, stateFrom(st))
}

func (a *Auth) handleSetToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, AuthState{Message: "Не удалось прочитать запрос."})
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeJSON(w, http.StatusBadRequest, AuthState{Message: "Пустой токен."})
		return
	}

	st, err := a.verify(r.Context(), token)
	if err != nil {
		msg := "Токен не принят Яндексом."
		if errors.Is(err, ymapi.ErrForbidden) {
			msg = "Токен принят, но доступ запрещён — проверьте регион и подписку."
		}
		writeJSON(w, http.StatusUnauthorized, AuthState{Message: msg, AuthURL: AuthorizeURL})
		return
	}
	if err := a.store.Set(token); err != nil {
		writeJSON(w, http.StatusInternalServerError, AuthState{Message: "Не удалось сохранить токен: " + err.Error()})
		return
	}
	a.remember(st)
	writeJSON(w, http.StatusOK, stateFrom(st))
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(); err != nil {
		writeJSON(w, http.StatusInternalServerError, AuthState{Message: err.Error()})
		return
	}
	a.remember(nil)
	writeJSON(w, http.StatusOK, AuthState{Message: "Токен удалён.", AuthURL: AuthorizeURL})
}

func (a *Auth) remember(st *ymapi.AccountStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = st
}

func stateFrom(st *ymapi.AccountStatus) AuthState {
	out := AuthState{
		Authorized: true,
		Login:      st.Login,
		HasPlus:    st.HasPlus,
		Region:     st.Region,
	}
	if !st.HasPlus {
		out.Message = "Подписка Плюс неактивна — воспроизведение будет недоступно."
	}
	return out
}

// writeJSON — единая точка сериализации ответов.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 4: Запустить тесты**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add internal/httpapi/auth.go internal/httpapi/auth_test.go
git commit -m "feat(httpapi): ввод и проверка токена, токен не покидает демон"
```

---

### Task 12: HTTP API плеера и поток состояния

**Files:**
- Create: `internal/httpapi/sse.go`, `internal/httpapi/routes.go`, `internal/httpapi/routes_test.go`
- Modify: `internal/httpapi/server.go` — без изменений API, только если понадобится

**Interfaces:**
- Consumes: `Auth` (Task 11), `player.Queue`/`player.State` (Task 4), `stream.Proxy` (Task 8), `ymapi.Client` (Tasks 5, 9, 10)
- Produces:
  - `httpapi.NewHub() *Hub` с `Subscribe() (<-chan player.State, func())`, `Broadcast(player.State)`, `HandleSSE(http.ResponseWriter, *http.Request)`
  - `httpapi.App` с полями `Auth *Auth`, `Queue *player.Queue`, `Hub *Hub`, `Buffer *stream.Buffer`, `Proxy *stream.Proxy`, `Client func() (*ymapi.Client, error)`
  - `(*App).Routes() *http.ServeMux`

**Разделение ответственности между демоном и фронтендом.** Демон владеет очередью, источником и фидбеком. Фронтенд владеет самим воспроизведением: он ставит `<audio src="/stream/{id}">`, знает позицию и сообщает о ней. Такое разделение убирает необходимость синхронизировать таймер на сервере с реальным положением в потоке.

- [ ] **Step 1: Написать падающий тест**

Create `internal/httpapi/routes_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"music212/internal/player"
)

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
```

Add helper to the same file:

```go
import "context"

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
```

- [ ] **Step 2: Запустить тест и убедиться, что падает**

Run: `go test ./internal/httpapi/ -run 'TestHub|TestSSE|TestPlayerVolume' -v`
Expected: FAIL — `undefined: NewHub`

- [ ] **Step 3: Реализовать SSE-хаб**

Create `internal/httpapi/sse.go`:

```go
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"music212/internal/player"
)

// subscriberBuffer — сколько кадров состояния держим для подписчика,
// прежде чем считать его медленным.
const subscriberBuffer = 8

// Hub рассылает состояние плеера всем открытым вкладкам.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan player.State]struct{}
}

// NewHub создаёт пустой хаб.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan player.State]struct{})}
}

// Subscribe регистрирует подписчика и возвращает функцию отписки.
func (h *Hub) Subscribe() (<-chan player.State, func()) {
	ch := make(chan player.State, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Broadcast рассылает состояние. Медленный подписчик пропускает кадр,
// но никогда не блокирует остальных: состояние самодостаточно, и потеря
// промежуточного кадра ничего не ломает.
func (h *Hub) Broadcast(st player.State) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

// HandleSSE держит открытое соединение и стримит состояние.
func (h *Hub) HandleSSE(w http.ResponseWriter, r *http.Request, initial player.State) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "стриминг не поддерживается", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := h.Subscribe()
	defer cancel()

	writeFrame(w, initial)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case st, open := <-ch:
			if !open {
				return
			}
			writeFrame(w, st)
			flusher.Flush()
		}
	}
}

func writeFrame(w http.ResponseWriter, st player.State) {
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
}
```

- [ ] **Step 4: Реализовать роуты**

Create `internal/httpapi/routes.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

// App связывает все модули и отдаёт роутер.
type App struct {
	Auth   *Auth
	Queue  *player.Queue
	Hub    *Hub
	Buffer *stream.Buffer
	Proxy  *stream.Proxy
	Client func() (*ymapi.Client, error)

	mu       sync.RWMutex
	status   player.Status
	position float64
	volume   float64
	errText  string
	batchID  string
}

// Routes собирает роутер демона.
func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	if a.Auth != nil {
		a.Auth.Register(mux)
	}
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/search", a.handleSearch)
	mux.HandleFunc("GET /api/playlists", a.handlePlaylists)
	mux.HandleFunc("GET /api/playlists/{kind}", a.handlePlaylistTracks)
	mux.HandleFunc("GET /api/likes", a.handleLikes)
	mux.HandleFunc("POST /api/play", a.handlePlay)
	mux.HandleFunc("POST /api/player/next", a.handleNext)
	mux.HandleFunc("POST /api/player/prev", a.handlePrev)
	mux.HandleFunc("POST /api/player/pause", a.handlePause)
	mux.HandleFunc("POST /api/player/resume", a.handleResume)
	mux.HandleFunc("POST /api/player/progress", a.handleProgress)
	mux.HandleFunc("POST /api/player/volume", a.handleVolume)
	mux.HandleFunc("GET /stream/{trackId}", a.handleStream)
	return mux
}

// snapshot собирает текущее состояние для отправки фронтенду.
func (a *App) snapshot() player.State {
	tracks, idx, source := a.Queue.Snapshot()
	a.mu.RLock()
	defer a.mu.RUnlock()

	st := player.State{
		Status:     a.status,
		Position:   a.position,
		Volume:     a.volume,
		Queue:      tracks,
		QueueIndex: idx,
		Source:     source,
		Error:      a.errText,
	}
	if st.Status == "" {
		st.Status = player.StatusIdle
	}
	if cur := a.Queue.Current(); cur != nil {
		st.Track = cur
		st.Duration = float64(cur.Duration)
	}
	return st
}

func (a *App) publish() { a.Hub.Broadcast(a.snapshot()) }

func (a *App) setStatus(s player.Status, errText string) {
	a.mu.Lock()
	a.status, a.errText = s, errText
	a.mu.Unlock()
	a.publish()
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	a.Hub.HandleSSE(w, r, a.snapshot())
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой запрос"})
		return
	}
	tracks, err := c.SearchTracks(r.Context(), text)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (a *App) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	st := a.Auth.Status()
	if st == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "нет статуса аккаунта"})
		return
	}
	lists, err := c.UserPlaylists(r.Context(), st.UID)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

func (a *App) handlePlaylistTracks(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	kind, convErr := strconv.Atoi(r.PathValue("kind"))
	if convErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный kind"})
		return
	}
	tracks, err := c.PlaylistTracks(r.Context(), a.Auth.Status().UID, kind)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (a *App) handleLikes(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	tracks, err := c.LikedTracks(r.Context(), a.Auth.Status().UID)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handlePlay строит очередь из указанного источника.
func (a *App) handlePlay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string         `json:"source"`
		Kind   int            `json:"kind"`
		Query  string         `json:"query"`
		Tracks []player.Track `json:"tracks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать запрос"})
		return
	}
	c, err := a.client(w)
	if err != nil {
		return
	}

	var tracks []player.Track
	switch body.Source {
	case "wave":
		batch, wErr := c.StationTracks(r.Context(), ymapi.WaveStationID, "")
		if wErr != nil {
			a.apiError(w, wErr)
			return
		}
		a.mu.Lock()
		a.batchID = batch.BatchID
		a.mu.Unlock()
		tracks = batch.Tracks
		go c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch.BatchID, ymapi.EventRadioStarted, "", 0)
	case "playlist":
		tracks, err = c.PlaylistTracks(r.Context(), a.Auth.Status().UID, body.Kind)
	case "likes":
		tracks, err = c.LikedTracks(r.Context(), a.Auth.Status().UID)
	case "search":
		tracks, err = c.SearchTracks(r.Context(), body.Query)
	case "tracks":
		tracks = body.Tracks
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неизвестный источник"})
		return
	}
	if err != nil {
		a.apiError(w, err)
		return
	}
	if len(tracks) == 0 {
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}

	a.Queue.Set(tracks, body.Source)
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleNext(w http.ResponseWriter, r *http.Request) {
	a.reportFinished(r.Context(), ymapi.EventTrackFinished)
	if !a.Queue.Next() {
		a.setStatus(player.StatusIdle, "")
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}
	a.refillWave(r.Context())
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handlePrev(w http.ResponseWriter, r *http.Request) {
	a.Queue.Prev()
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handlePause(w http.ResponseWriter, r *http.Request) {
	a.setStatus(player.StatusPaused, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleResume(w http.ResponseWriter, r *http.Request) {
	a.setStatus(player.StatusPlaying, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

// handleProgress принимает позицию от фронтенда: воспроизведением владеет он.
func (a *App) handleProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Position float64 `json:"position"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
	a.mu.Lock()
	a.position = body.Position
	a.status = player.StatusPlaying
	a.mu.Unlock()
	a.publish()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Volume float64 `json:"volume"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать запрос"})
		return
	}
	if body.Volume < 0 {
		body.Volume = 0
	}
	if body.Volume > 1 {
		body.Volume = 1
	}
	a.mu.Lock()
	a.volume = body.Volume
	a.mu.Unlock()
	a.publish()
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	if a.Proxy == nil {
		http.Error(w, "прокси не настроен", http.StatusServiceUnavailable)
		return
	}
	a.Proxy.ServeTrack(w, r, r.PathValue("trackId"))
}

// retainBuffer оставляет в буфере только текущий и следующий трек.
func (a *App) retainBuffer() {
	if a.Buffer == nil {
		return
	}
	tracks, idx, _ := a.Queue.Snapshot()
	keep := make([]string, 0, 2)
	for i := idx; i < idx+2 && i < len(tracks); i++ {
		keep = append(keep, tracks[i].ID)
	}
	a.Buffer.Retain(keep...)
}

// refillWave подкачивает следующий батч ротора, когда очередь подходит к концу.
func (a *App) refillWave(ctx context.Context) {
	_, _, source := a.Queue.Snapshot()
	if source != "wave" || a.Queue.Remaining() >= 2 {
		return
	}
	c, err := a.newClient()
	if err != nil {
		return
	}
	cur := a.Queue.Current()
	last := ""
	if cur != nil {
		last = cur.ID
	}
	batch, err := c.StationTracks(ctx, ymapi.WaveStationID, last)
	if err != nil || batch == nil {
		return
	}
	a.mu.Lock()
	a.batchID = batch.BatchID
	a.mu.Unlock()
	a.Queue.Append(batch.Tracks)
}

// reportFinished отправляет оба канала фидбека для текущего трека.
func (a *App) reportFinished(ctx context.Context, event string) {
	cur := a.Queue.Current()
	if cur == nil {
		return
	}
	c, err := a.newClient()
	if err != nil {
		return
	}
	a.mu.RLock()
	pos, batch := a.position, a.batchID
	a.mu.RUnlock()

	_, _, source := a.Queue.Snapshot()
	go func() {
		if source == "wave" {
			c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch, event, cur.ID, pos)
		}
		c.PlayAudio(context.Background(), ymapi.PlayEvent{
			TrackID:       cur.ID,
			From:          source,
			PlayedSeconds: pos,
			TotalSeconds:  float64(cur.Duration),
		})
	}()
	_ = ctx
}

func (a *App) newClient() (*ymapi.Client, error) {
	if a.Client == nil {
		return nil, ymapi.ErrUnauthorized
	}
	return a.Client()
}

// client достаёт клиент и сам отвечает 401, если токена нет.
func (a *App) client(w http.ResponseWriter) (*ymapi.Client, error) {
	c, err := a.newClient()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "нужен токен"})
		return nil, err
	}
	return c, nil
}

func (a *App) apiError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	switch {
	case errorsIs(err, ymapi.ErrUnauthorized):
		code = http.StatusUnauthorized
	case errorsIs(err, ymapi.ErrForbidden):
		code = http.StatusForbidden
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
```

Add to the same file:

```go
import "errors"

func errorsIs(err, target error) bool { return errors.Is(err, target) }
```

- [ ] **Step 5: Запустить тесты**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Коммит**

```bash
git add internal/httpapi
git commit -m "feat(httpapi): роуты плеера и поток состояния через SSE"
```

---

### Task 13: Фронтенд

**Files:**
- Create: `web/index.html`, `web/src/app.ts`, `web/package.json`, `internal/httpapi/static.go`
- Modify: `Makefile` — добавить цель `web`

**Interfaces:**
- Consumes: HTTP API (Task 12), `/stream/{trackId}` (Task 8)
- Produces: `httpapi.StaticHandler() http.Handler` — отдаёт вшитые файлы из `web/dist`

> Vanilla TypeScript, без фреймворков (см. Global Constraints). Состояние приходит по SSE и не дублируется на клиенте: страница только рисует. Никаких промо-блоков — это прямое требование.

- [ ] **Step 1: Создать манифест сборки**

Create `web/package.json`:

```json
{
  "name": "music212-web",
  "private": true,
  "scripts": {
    "build": "esbuild src/app.ts --bundle --minify --outfile=dist/app.js && cp index.html dist/index.html"
  },
  "devDependencies": {
    "esbuild": "^0.25.0",
    "typescript": "^5.7.0"
  }
}
```

- [ ] **Step 2: Создать разметку**

Create `web/index.html`:

```html
<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Плеер</title>
  <style>
    :root { --bg:#111; --fg:#eee; --dim:#888; --accent:#ffcc00; }
    @media (prefers-color-scheme: light) { :root { --bg:#fafafa; --fg:#111; --dim:#666; } }
    * { box-sizing: border-box; }
    body { margin:0; background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, sans-serif; }
    header { display:flex; gap:8px; padding:12px; border-bottom:1px solid #3333; }
    button { background:none; border:1px solid #6666; color:inherit; border-radius:6px;
             padding:6px 12px; cursor:pointer; font:inherit; }
    button:hover { border-color: var(--accent); }
    main { padding:16px; }
    #now { display:flex; gap:16px; align-items:center; margin-bottom:16px; }
    #cover { width:96px; height:96px; border-radius:8px; background:#3333; object-fit:cover; }
    #title { font-weight:600; }
    #artist { color:var(--dim); }
    #progress { width:100%; }
    ul { list-style:none; padding:0; margin:0; }
    li { padding:8px; border-radius:6px; cursor:pointer; }
    li:hover { background:#8881; }
    li.current { color:var(--accent); }
    #auth { padding:24px; max-width:560px; }
    input { width:100%; padding:8px; font:inherit; background:#8881;
            border:1px solid #6666; border-radius:6px; color:inherit; }
    .hidden { display:none; }
  </style>
</head>
<body>
  <div id="auth" class="hidden">
    <h2>Нужен токен</h2>
    <p>Откройте <a id="authLink" target="_blank" rel="noopener">страницу входа Яндекса</a>,
       войдите и скопируйте значение <code>access_token</code> из адресной строки.</p>
    <input id="tokenInput" placeholder="Вставьте токен сюда" autocomplete="off">
    <p><button id="tokenSave">Сохранить</button> <span id="authMsg"></span></p>
  </div>

  <div id="app" class="hidden">
    <header>
      <button id="btnWave">Моя волна</button>
      <button id="btnLikes">Мне нравится</button>
      <button id="btnLists">Плейлисты</button>
      <input id="search" placeholder="Поиск" style="max-width:240px">
    </header>
    <main>
      <section id="now">
        <img id="cover" alt="">
        <div style="flex:1">
          <div id="title">—</div>
          <div id="artist"></div>
          <input id="progress" type="range" min="0" max="100" value="0" step="0.1">
          <div>
            <button id="btnPrev">◀◀</button>
            <button id="btnPlay">▶</button>
            <button id="btnNext">▶▶</button>
            <input id="volume" type="range" min="0" max="1" step="0.01" value="1" style="width:100px">
          </div>
        </div>
      </section>
      <ul id="list"></ul>
    </main>
  </div>

  <audio id="audio"></audio>
  <script src="./app.js"></script>
</body>
</html>
```

- [ ] **Step 3: Написать клиент**

Create `web/src/app.ts`:

```ts
// Фронтенд владеет воспроизведением; демон владеет очередью и фидбеком.
// Состояние приходит по SSE и здесь не дублируется.

interface Track {
  id: string; title: string; artists: string[];
  album: string; coverUrl: string; duration: number; available: boolean;
}

interface State {
  status: string; track: Track | null; position: number; duration: number;
  volume: number; queue: Track[]; queueIndex: number; source: string; error?: string;
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const audio = $<HTMLAudioElement>("audio");
const listEl = $<HTMLUListElement>("list");
let currentTrackId = "";

async function api(path: string, body?: unknown): Promise<any> {
  const res = await fetch(path, body === undefined ? {} : {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.status === 204 ? null : res.json();
}

// --- авторизация ---

async function checkAuth(): Promise<boolean> {
  const st = await (await fetch("/api/auth/status")).json();
  if (st.authorized) {
    $("auth").classList.add("hidden");
    $("app").classList.remove("hidden");
    return true;
  }
  $("auth").classList.remove("hidden");
  $("app").classList.add("hidden");
  $("authMsg").textContent = st.message ?? "";
  ($("authLink") as HTMLAnchorElement).href = st.authUrl ?? "#";
  return false;
}

$("tokenSave").addEventListener("click", async () => {
  const token = ($("tokenInput") as HTMLInputElement).value.trim();
  const res = await fetch("/api/auth/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  const st = await res.json();
  $("authMsg").textContent = st.message ?? "";
  if (res.ok) { await checkAuth(); connect(); }
});

// --- отрисовка ---

function render(st: State) {
  const t = st.track;
  $("title").textContent = t ? t.title : "—";
  $("artist").textContent = t ? t.artists.join(", ") : (st.error ?? "");
  ($("cover") as HTMLImageElement).src = t?.coverUrl ?? "";
  $<HTMLButtonElement>("btnPlay").textContent = st.status === "playing" ? "⏸" : "▶";

  listEl.innerHTML = "";
  st.queue.forEach((track, i) => {
    const li = document.createElement("li");
    li.textContent = `${track.title} — ${track.artists.join(", ")}`;
    if (i === st.queueIndex) li.className = "current";
    li.addEventListener("click", () => api("/api/play", { source: "tracks", tracks: st.queue.slice(i) }));
    listEl.appendChild(li);
  });

  // Смена трека — единственный повод трогать src: иначе перезапустим текущий.
  if (t && t.id !== currentTrackId) {
    currentTrackId = t.id;
    audio.src = `/stream/${t.id}`;
    audio.play().catch(() => {});
    updateMediaSession(t);
  }
  if (!t) { currentTrackId = ""; audio.removeAttribute("src"); }
}

// MediaSession даёт медиа-клавиши и карточку «сейчас играет» в системе —
// то, чего обычная вкладка иначе не умеет.
function updateMediaSession(t: Track) {
  if (!("mediaSession" in navigator)) return;
  navigator.mediaSession.metadata = new MediaMetadata({
    title: t.title,
    artist: t.artists.join(", "),
    album: t.album,
    artwork: t.coverUrl ? [{ src: t.coverUrl, sizes: "400x400", type: "image/jpeg" }] : [],
  });
  navigator.mediaSession.setActionHandler("play", () => { audio.play(); api("/api/player/resume"); });
  navigator.mediaSession.setActionHandler("pause", () => { audio.pause(); api("/api/player/pause"); });
  navigator.mediaSession.setActionHandler("nexttrack", () => api("/api/player/next"));
  navigator.mediaSession.setActionHandler("previoustrack", () => api("/api/player/prev"));
}

// --- поток состояния ---

let source: EventSource | null = null;

function connect() {
  source?.close();
  source = new EventSource("/api/events");
  source.onmessage = (e) => render(JSON.parse(e.data) as State);
  source.onerror = () => { source?.close(); setTimeout(connect, 2000); };
}

// --- управление ---

$("btnWave").addEventListener("click", () => api("/api/play", { source: "wave" }));
$("btnLikes").addEventListener("click", () => api("/api/play", { source: "likes" }));
$("btnNext").addEventListener("click", () => api("/api/player/next"));
$("btnPrev").addEventListener("click", () => api("/api/player/prev"));

$("btnPlay").addEventListener("click", () => {
  if (audio.paused) { audio.play(); api("/api/player/resume"); }
  else { audio.pause(); api("/api/player/pause"); }
});

$("btnLists").addEventListener("click", async () => {
  const lists = await (await fetch("/api/playlists")).json();
  listEl.innerHTML = "";
  for (const pl of lists) {
    const li = document.createElement("li");
    li.textContent = `${pl.title} (${pl.trackCount})`;
    li.addEventListener("click", () => api("/api/play", { source: "playlist", kind: pl.kind }));
    listEl.appendChild(li);
  }
});

$("search").addEventListener("keydown", (e) => {
  if ((e as KeyboardEvent).key !== "Enter") return;
  const q = ($("search") as HTMLInputElement).value.trim();
  if (q) api("/api/play", { source: "search", query: q });
});

$("volume").addEventListener("input", () => {
  const v = parseFloat(($("volume") as HTMLInputElement).value);
  audio.volume = v;
  api("/api/player/volume", { volume: v });
});

$("progress").addEventListener("input", () => {
  const pct = parseFloat(($("progress") as HTMLInputElement).value);
  if (audio.duration) audio.currentTime = (pct / 100) * audio.duration;
});

audio.addEventListener("ended", () => api("/api/player/next"));
audio.addEventListener("error", () => api("/api/player/next"));

// Позицию знает только фронтенд — сообщаем её раз в пять секунд.
setInterval(() => {
  if (!audio.paused && audio.currentTime > 0) {
    api("/api/player/progress", { position: audio.currentTime });
  }
  if (audio.duration) {
    ($("progress") as HTMLInputElement).value = String((audio.currentTime / audio.duration) * 100);
  }
}, 5000);

checkAuth().then((ok) => { if (ok) connect(); });
```

- [ ] **Step 4: Вшить статику в бинарь**

Create `internal/httpapi/static.go`:

```go
package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distFS embed.FS

// StaticHandler отдаёт собранный фронтенд из вшитой файловой системы.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
```

- [ ] **Step 5: Настроить сборку**

Фронтенд собирается в `internal/httpapi/dist`, чтобы `//go:embed` его видел — директива не умеет подниматься выше своего пакета.

Modify `web/package.json`, заменив скрипт `build`:

```json
"build": "esbuild src/app.ts --bundle --minify --outfile=../internal/httpapi/dist/app.js && cp index.html ../internal/httpapi/dist/index.html"
```

Modify `Makefile`:

```make
.PHONY: test build run web
web:
	cd web && npx --yes esbuild src/app.ts --bundle --minify --outfile=../internal/httpapi/dist/app.js
	cp web/index.html internal/httpapi/dist/index.html
test:
	go test ./... -count=1
build: web
	go build -o musicd ./cmd/musicd
run: build
	./musicd
```

Modify `.gitignore`, добавив строку:

```
internal/httpapi/dist/
```

- [ ] **Step 6: Собрать и проверить**

```bash
mkdir -p internal/httpapi/dist
make web
go build ./...
```
Expected: сборка проходит, `internal/httpapi/dist/app.js` и `index.html` созданы

- [ ] **Step 7: Коммит**

```bash
git add web internal/httpapi/static.go Makefile .gitignore
git commit -m "feat(web): интерфейс плеера, SSE-клиент и MediaSession"
```

---

### Task 14: Сборка воедино и живая проверка

**Files:**
- Modify: `cmd/musicd/main.go` — собрать все модули
- Create: `internal/ymapi/live_test.go` — живой смоук за build-тегом
- Create: `docs/research/get-file-info-probe.md` — результат пробы новой схемы

**Interfaces:**
- Consumes: всё предыдущее
- Produces: рабочий бинарь `musicd`

- [ ] **Step 1: Собрать зависимости в main**

Replace `cmd/musicd/main.go`:

```go
// Command musicd — локальный демон плеера Яндекс Музыки.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"music212/internal/auth"
	"music212/internal/httpapi"
	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

func main() {
	noKeychain := flag.Bool("no-keychain", false, "хранить токен только в памяти процесса")
	noOpen := flag.Bool("no-open", false, "не открывать браузер при старте")
	flag.Parse()

	var store auth.Store = auth.NewKeyring()
	if *noKeychain {
		store = auth.NewMemory()
	}

	authHandler := httpapi.NewAuth(store, httpapi.DefaultVerify)
	buffer := stream.NewBuffer(stream.DefaultMaxBytes)

	newClient := func() (*ymapi.Client, error) {
		token, err := authHandler.Token()
		if err != nil {
			return nil, err
		}
		return ymapi.New(token), nil
	}

	app := &httpapi.App{
		Auth:   authHandler,
		Queue:  player.NewQueue(),
		Hub:    httpapi.NewHub(),
		Buffer: buffer,
		Client: newClient,
	}
	app.Proxy = stream.NewProxy(resolverFunc(newClient), buffer, nil)

	mux := app.Routes()
	mux.Handle("/", httpapi.StaticHandler())

	srv := httpapi.New(mux)
	if err := srv.Start(); err != nil {
		log.Fatalf("не удалось запустить сервер: %v", err)
	}
	url := "http://" + srv.Addr()
	fmt.Printf("плеер слушает %s\n", url)
	if !*noOpen {
		openBrowser(url)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	fmt.Println("\nостанавливаюсь…")
	buffer.Clear() // буфер не переживает завершение — это требование скоупа
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// resolverFunc адаптирует фабрику клиентов к интерфейсу stream.Resolver.
type resolverFunc func() (*ymapi.Client, error)

func (f resolverFunc) ResolveTrack(ctx context.Context, trackID string) (string, error) {
	c, err := f()
	if err != nil {
		return "", err
	}
	return c.ResolveTrack(ctx, trackID)
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "explorer"
	default:
		cmd = "xdg-open"
	}
	exec.Command(cmd, url).Start()
	_ = http.DefaultClient
}
```

- [ ] **Step 2: Написать живой смоук**

Create `internal/ymapi/live_test.go`:

```go
//go:build live

// Живой смоук против настоящего API. Не запускается в обычном прогоне.
// Запуск: YM_TOKEN=<токен> go test -tags live ./internal/ymapi/ -run TestLive -v
package ymapi

import (
	"context"
	"os"
	"testing"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	token := os.Getenv("YM_TOKEN")
	if token == "" {
		t.Skip("YM_TOKEN не задан")
	}
	return New(token)
}

func TestLiveAccountStatus(t *testing.T) {
	st, err := liveClient(t).AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	t.Logf("аккаунт: login=%s region=%d plus=%v", st.Login, st.Region, st.HasPlus)
	if !st.HasPlus {
		t.Fatal("нет подписки Плюс — воспроизведение работать не будет")
	}
}

// Замыкает шагающий скелет: волна -> трек -> подписанная ссылка.
func TestLiveWalkingSkeleton(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	batch, err := c.StationTracks(ctx, WaveStationID, "")
	if err != nil {
		t.Fatalf("StationTracks: %v", err)
	}
	if len(batch.Tracks) == 0 {
		t.Fatal("волна вернула пустой батч")
	}
	track := batch.Tracks[0]
	t.Logf("трек: %s — %v", track.Title, track.Artists)

	variants, err := c.DownloadVariants(ctx, track.ID)
	if err != nil {
		t.Fatalf("DownloadVariants: %v", err)
	}
	for _, v := range variants {
		t.Logf("вариант: codec=%s bitrate=%d preview=%v", v.Codec, v.BitrateKbps, v.Preview)
	}

	link, err := c.ResolveTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("ResolveTrack: %v", err)
	}
	if len(link) < 20 {
		t.Fatalf("подозрительная ссылка: %q", link)
	}
	t.Log("шагающий скелет замкнулся: подписанная ссылка получена")
}
```

- [ ] **Step 3: Прогнать живой смоук**

```bash
YM_TOKEN=<ваш токен> go test -tags live ./internal/ymapi/ -run TestLive -v
```
Expected: PASS, в логах видны доступные варианты качества

Если тест падает — **остановиться и разобраться здесь**, не переходя к следующим шагам. Именно эта точка отделяет «протокол понят» от «протокол угадан».

- [ ] **Step 4: Собрать и проверить вживую**

```bash
make build && ./musicd
```
Expected: браузер открывается, экран ввода токена работает, после вставки токена «Моя волна» играет

- [ ] **Step 5: Проба новой схемы `get-file-info`**

> **Условие остановки (§2, §5.1 спеки).** Цель шага — выяснить, отдаёт ли API высокое качество в открытом виде. Если ответ приходит под технической защитой, шаг закрывается с отрицательным результатом: обход защиты не реализуется. Плеер к этому моменту уже полностью работает на проверенной схеме.

Записать результат в `docs/research/get-file-info-probe.md`: какой запрос отправлялся, что ответил API, подтвердились ли ключ и порядок полей из §5.1, и вывод — реализуемо ли в открытом виде.

- [ ] **Step 6: Финальный прогон и коммит**

```bash
go vet ./...
go test ./... -count=1
git add -A
git commit -m "feat: сборка демона воедино и живая проверка протокола"
```

---

## Самопроверка плана

**Покрытие спеки.** §3 архитектура — Tasks 1, 14. §4 модули — Tasks 2–12 по одному на модуль. §5.1 подписи — Task 2 (обе схемы), проба новой — Task 14. §5.2 аудио-прокси — Task 8. §5.3 буфер — Task 7 плюс `retainBuffer` в Task 12. §5.4 волна и два канала фидбека — Task 10, использование в Task 12. §6 авторизация — Task 11, с зафиксированным отклонением от спеки. §7 состояние — Task 4. §8 HTTP API — Tasks 11, 12. §9 фронтенд и MediaSession — Task 13. §10 обработка ошибок — распределена: 401/403 в Task 5, 410 в Task 8, экран входа в Task 11, скип недоступного в Task 12. §11 тестирование — golden-тесты в Task 2, фикстуры в Tasks 5–10, живой смоук в Task 14.

**Незакрытое.** «Дизлайк» из §8 спеки в план не попал: он требует эндпоинта лайков, которого нет ни в одной задаче. Добавить отдельной задачей после v1 либо расширить Task 9 — решение за исполнителем, на воспроизведение это не влияет.

**Согласованность имён.** `player.Track` используется единообразно во всех модулях; `ymapi` возвращает именно её, а не свою форму. `Resolver.ResolveTrack` в Task 8 и `(*Client).ResolveTrack` в Task 6 совпадают по сигнатуре. `stream.NewBuffer`/`Retain`/`Clear` вызываются в Task 12 и Task 14 ровно теми именами, что заданы в Task 7.
