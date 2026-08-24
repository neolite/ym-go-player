# Навигация по артистам и альбомам — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Клик по имени артиста или названию альбома открывает карточку с плоским списком его треков, не прерывая текущее воспроизведение; клик по треку внутри карточки заменяет очередь и играет с него.

**Architecture:** Два новых тонких GET-роута на бэкенде (`/api/artists/{id}/tracks`, `/api/albums/{id}/tracks`), каждый — обёртка над новым методом `ymapi.Client`. `player.Track` получает `ArtistIDs`/`AlbumID` параллельно уже существующим `Artists`/`Album`. На фронтенде — кликабельные имена артистов/альбомов везде, где сейчас рисуется строка трека, открывающие карточку поверх уже существующей панели `#lists`; клик по треку в карточке идёт через уже существующий `POST /api/play {"source":"tracks",...}`.

**Tech Stack:** Go 1.x (backend), vanilla TypeScript + esbuild (frontend), стандартный `net/http`, без новых зависимостей.

**Spec:** [`docs/superpowers/specs/2026-08-24-artist-album-navigation-design.md`](../specs/2026-08-24-artist-album-navigation-design.md)

## Global Constraints

- Треки артиста — один запрос, `page-size=100`, без пагинации на нашей стороне (спека §2/§5).
- Треки альбома — `volumes: [][]Track` разворачивается в плоский список, порядок дисков и треков внутри диска сохраняется (спека §5).
- Новые роуты — публичный каталог, без `requireUID` (спека §5).
- `handlePlay` не меняется: карточка отдаёт треки через уже существующий `POST /api/play {"source":"tracks","tracks":[...]}` (спека §5/§6).
- `Artists []string`/`Album string` не переделываются в структуры `{id,name}` — добавляются параллельные `ArtistIDs`/`AlbumID` (спека §4).
- Фронтенд без автотестов — ручная проверка через `tsc --noEmit` + `npm run build` + claude-in-chrome (established pattern этой сессии, спека §8).

---

## Task 1: Данные — ArtistIDs/AlbumID в Track

**Files:**
- Modify: `internal/player/state.go` (структура `Track`)
- Modify: `internal/ymapi/types.go` (`apiTrack`, `toPlayer`)
- Modify: `internal/ymapi/tracks_test.go` (`tracksFixture`, `TestTracksParsesFixture`)

**Interfaces:**
- Produces: `player.Track.ArtistIDs []string` (тот же порядок/длина, что `Artists`), `player.Track.AlbumID string` (пусто, если альбома нет).

- [ ] **Step 1: Расширить фикстуру и тест новыми полями (RED)**

В `internal/ymapi/tracks_test.go` заменить `tracksFixture` и добавить проверки в `TestTracksParsesFixture`:

```go
const tracksFixture = `{"result":[{
  "id":"12345","title":"Тестовый трек","available":true,"durationMs":183000,
  "coverUri":"avatars.example.net/get-music-content/1/%%",
  "artists":[{"id":111,"name":"Первый"},{"id":222,"name":"Второй"}],
  "albums":[{"id":333,"title":"Альбом"}]
}]}`
```

В конец `TestTracksParsesFixture` (после существующей проверки `tr.CoverURL`) добавить:

```go
	if len(tr.ArtistIDs) != 2 || tr.ArtistIDs[0] != "111" || tr.ArtistIDs[1] != "222" {
		t.Fatalf("artistIDs = %v", tr.ArtistIDs)
	}
	if tr.AlbumID != "333" {
		t.Fatalf("albumID = %q, want 333", tr.AlbumID)
	}
```

- [ ] **Step 2: Запустить тест, убедиться, что падает (RED)**

Run: `go test ./internal/ymapi/... -run TestTracksParsesFixture -v`
Expected: FAIL — `tr.ArtistIDs undefined` (поля ещё нет в `player.Track`) либо compile error.

- [ ] **Step 3: Добавить поля в player.Track**

В `internal/player/state.go`:

```go
// Track — минимум метаданных, нужный для отрисовки и воспроизведения.
type Track struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
	ArtistIDs []string `json:"artistIds"`
	Album     string   `json:"album"`
	AlbumID   string   `json:"albumId"`
	CoverURL  string   `json:"coverUrl"`
	Duration  int      `json:"duration"`
	Available bool     `json:"available"`
	Liked     bool     `json:"liked"`
}
```

- [ ] **Step 4: Парсить id в apiTrack.toPlayer()**

В `internal/ymapi/types.go` — расширить `apiTrack` и `toPlayer()`:

```go
type apiTrack struct {
	ID         any    `json:"id"`
	Title      string `json:"title"`
	Available  bool   `json:"available"`
	DurationMs int    `json:"durationMs"`
	CoverURI   string `json:"coverUri"`
	Artists    []struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
	Albums []struct {
		ID    any    `json:"id"`
		Title string `json:"title"`
	} `json:"albums"`
}

func (t apiTrack) toPlayer() player.Track {
	artists := make([]string, 0, len(t.Artists))
	artistIDs := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		artists = append(artists, a.Name)
		artistIDs = append(artistIDs, idString(a.ID))
	}
	album, albumID := "", ""
	if len(t.Albums) > 0 {
		album = t.Albums[0].Title
		albumID = idString(t.Albums[0].ID)
	}
	return player.Track{
		ID:        idString(t.ID),
		Title:     t.Title,
		Artists:   artists,
		ArtistIDs: artistIDs,
		Album:     album,
		AlbumID:   albumID,
		CoverURL:  coverURL(t.CoverURI),
		Duration:  t.DurationMs / 1000,
		Available: t.Available,
	}
}
```

- [ ] **Step 5: Запустить тест, убедиться, что проходит (GREEN)**

Run: `go test ./internal/ymapi/... -run TestTracksParsesFixture -v`
Expected: PASS

- [ ] **Step 6: Прогнать весь пакет ymapi — фикстура использовалась и другими тестами**

Run: `go test ./internal/ymapi/... -v 2>&1 | tail -40`
Expected: все тесты пакета проходят (фикстура `tracksFixture` могла использоваться и в других тестах этого файла — добавление полей `id` в JSON не должно было их сломать, но убедиться нужно явно).

- [ ] **Step 7: Прогнать весь бэкенд и закоммитить**

Run: `go build ./... && go test ./... 2>&1 | tail -10`
Expected: `Success` / все тесты проходят.

```bash
git add internal/player/state.go internal/ymapi/types.go internal/ymapi/tracks_test.go
git commit -m "feat(ymapi): парсить id артиста и альбома в Track"
```

---

## Task 2: ymapi.ArtistTracks

**Files:**
- Create: `internal/ymapi/artists.go`
- Create: `internal/ymapi/artists_test.go`

**Interfaces:**
- Consumes: `apiTrack.toPlayer()` (Task 1), `Client.Get(ctx, path, url.Values, out)` (существующий, см. `internal/ymapi/search.go`).
- Produces: `func (c *Client) ArtistTracks(ctx context.Context, artistID string) ([]player.Track, error)`.

- [ ] **Step 1: Написать падающий тест (RED)**

`internal/ymapi/artists_test.go`:

```go
package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const artistTracksFixture = `{"result":{"tracks":[
  {"id":"1","title":"Первый трек","available":true,"durationMs":200000,
   "artists":[{"id":"777","name":"Артист"}],"albums":[{"id":"888","title":"Альбом"}]},
  {"id":"2","title":"Второй трек","available":true,"durationMs":180000,
   "artists":[{"id":"777","name":"Артист"}],"albums":[]}
],"pager":{"total":2,"page":0,"perPage":100}}}`

func TestArtistTracks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artists/777/tracks" {
			t.Errorf("path = %q, want /artists/777/tracks", r.URL.Path)
		}
		if got := r.URL.Query().Get("page-size"); got != "100" {
			t.Errorf("page-size = %q, want 100", got)
		}
		w.Write([]byte(artistTracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).ArtistTracks(context.Background(), "777")
	if err != nil {
		t.Fatalf("ArtistTracks: %v", err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("треки = %+v", got)
	}
	if len(got[0].ArtistIDs) != 1 || got[0].ArtistIDs[0] != "777" {
		t.Fatalf("artistIDs = %v", got[0].ArtistIDs)
	}
}
```

- [ ] **Step 2: Убедиться, что падает (RED)**

Run: `go test ./internal/ymapi/... -run TestArtistTracks -v`
Expected: FAIL — `ArtistTracks undefined` (build failed).

- [ ] **Step 3: Реализовать ArtistTracks**

`internal/ymapi/artists.go`:

```go
package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// ArtistTracks отдаёт треки артиста — карточка «клик по имени», не полный
// каталог: page-size фиксирован, пагинации на нашей стороне нет (спека
// docs/superpowers/specs/2026-08-24-artist-album-navigation-design.md §2/§5).
func (c *Client) ArtistTracks(ctx context.Context, artistID string) ([]player.Track, error) {
	q := url.Values{"page": {"0"}, "page-size": {"100"}}
	var res struct {
		Tracks []apiTrack `json:"tracks"`
	}
	if err := c.Get(ctx, "/artists/"+artistID+"/tracks", q, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks))
	for _, t := range res.Tracks {
		out = append(out, t.toPlayer())
	}
	return out, nil
}
```

- [ ] **Step 4: Убедиться, что проходит (GREEN)**

Run: `go test ./internal/ymapi/... -run TestArtistTracks -v`
Expected: PASS

- [ ] **Step 5: Коммит**

```bash
git add internal/ymapi/artists.go internal/ymapi/artists_test.go
git commit -m "feat(ymapi): ArtistTracks — треки артиста для карточки"
```

---

## Task 3: ymapi.AlbumTracks

**Files:**
- Create: `internal/ymapi/albums.go`
- Create: `internal/ymapi/albums_test.go`

**Interfaces:**
- Consumes: `apiTrack.toPlayer()` (Task 1), `Client.Get` (существующий).
- Produces: `func (c *Client) AlbumTracks(ctx context.Context, albumID string) ([]player.Track, error)`.

- [ ] **Step 1: Написать падающий тест (RED)**

`internal/ymapi/albums_test.go` — два «диска» в `volumes`, проверяем, что результат — плоский список в исходном порядке:

```go
package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const albumTracksFixture = `{"result":{"id":"999","title":"Альбом","volumes":[
  [
    {"id":"1","title":"Трек 1","available":true,"durationMs":200000,"artists":[],"albums":[]},
    {"id":"2","title":"Трек 2","available":true,"durationMs":180000,"artists":[],"albums":[]}
  ],
  [
    {"id":"3","title":"Трек 3 (диск 2)","available":true,"durationMs":210000,"artists":[],"albums":[]}
  ]
]}}`

func TestAlbumTracksFlattensVolumes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/albums/999/with-tracks" {
			t.Errorf("path = %q, want /albums/999/with-tracks", r.URL.Path)
		}
		w.Write([]byte(albumTracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).AlbumTracks(context.Background(), "999")
	if err != nil {
		t.Fatalf("AlbumTracks: %v", err)
	}
	want := []string{"1", "2", "3"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; треки = %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("треки[%d].ID = %q, want %q (порядок дисков/треков не сохранён)", i, got[i].ID, id)
		}
	}
}
```

- [ ] **Step 2: Убедиться, что падает (RED)**

Run: `go test ./internal/ymapi/... -run TestAlbumTracksFlattensVolumes -v`
Expected: FAIL — `AlbumTracks undefined`.

- [ ] **Step 3: Реализовать AlbumTracks**

`internal/ymapi/albums.go`:

```go
package ymapi

import (
	"context"

	"music212/internal/player"
)

// AlbumTracks отдаёт треки альбома для карточки, одним плоским списком.
// Yandex возвращает альбом дисками (volumes) — здесь они разворачиваются
// в один список, порядок дисков и треков внутри диска сохраняется
// (спека docs/superpowers/specs/2026-08-24-artist-album-navigation-design.md §5).
func (c *Client) AlbumTracks(ctx context.Context, albumID string) ([]player.Track, error) {
	var res struct {
		Volumes [][]apiTrack `json:"volumes"`
	}
	if err := c.Get(ctx, "/albums/"+albumID+"/with-tracks", nil, &res); err != nil {
		return nil, err
	}
	var out []player.Track
	for _, vol := range res.Volumes {
		for _, t := range vol {
			out = append(out, t.toPlayer())
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Убедиться, что проходит (GREEN)**

Run: `go test ./internal/ymapi/... -run TestAlbumTracksFlattensVolumes -v`
Expected: PASS

- [ ] **Step 5: Прогнать весь пакет и закоммитить**

Run: `go test ./internal/ymapi/... 2>&1 | tail -10`
Expected: все тесты пакета проходят.

```bash
git add internal/ymapi/albums.go internal/ymapi/albums_test.go
git commit -m "feat(ymapi): AlbumTracks — треки альбома, диски сведены в список"
```

---

## Task 4: HTTP-роуты /api/artists/{id}/tracks и /api/albums/{id}/tracks

**Files:**
- Modify: `internal/httpapi/routes.go` (`Routes()`, новые хендлеры)
- Create: `internal/httpapi/artist_album_test.go`

**Interfaces:**
- Consumes: `ymapi.Client.ArtistTracks`/`AlbumTracks` (Task 2/3), `a.client(w)` (существующий, `internal/httpapi/routes.go`), `a.apiError`, `writeJSON`.
- Produces: `GET /api/artists/{artistId}/tracks`, `GET /api/albums/{albumId}/tracks` — оба отвечают `200` с `[]player.Track` в теле (та же форма, что `/api/likes`).

- [ ] **Step 1: Написать падающий тест (RED)**

`internal/httpapi/artist_album_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music212/internal/ymapi"
)

// Оба роута — публичный каталог: requireUID не нужен, только client(w).
func TestArtistAndAlbumTracksRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artists/777/tracks":
			w.Write([]byte(`{"result":{"tracks":[{"id":"1","title":"Трек артиста","available":true,"durationMs":100000,"artists":[],"albums":[]}]}}`))
		case "/albums/999/with-tracks":
			w.Write([]byte(`{"result":{"volumes":[[{"id":"2","title":"Трек альбома","available":true,"durationMs":100000,"artists":[],"albums":[]}]]}}`))
		default:
			t.Errorf("неожиданный путь: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	app := &App{Client: func() (*ymapi.Client, error) { return ymapi.NewWithBase("t", srv.URL), nil }}
	mux := app.Routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/artists/777/tracks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("артист: code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Трек артиста") {
		t.Fatalf("артист: тело не содержит трек: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/albums/999/tracks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("альбом: code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Трек альбома") {
		t.Fatalf("альбом: тело не содержит трек: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Убедиться, что падает (RED)**

Run: `go test ./internal/httpapi/... -run TestArtistAndAlbumTracksRoutes -v`
Expected: FAIL — 404 (роуты ещё не зарегистрированы) либо build error, если хендлеры ещё не существуют.

- [ ] **Step 3: Добавить хендлеры и зарегистрировать роуты**

В `internal/httpapi/routes.go`, в `Routes()` — после строки `mux.HandleFunc("GET /api/likes", a.handleLikes)`:

```go
	mux.HandleFunc("GET /api/artists/{artistId}/tracks", a.handleArtistTracks)
	mux.HandleFunc("GET /api/albums/{albumId}/tracks", a.handleAlbumTracks)
```

Рядом с `handleLikes` (после неё) — новые хендлеры:

```go
// handleArtistTracks отдаёт треки артиста для карточки «клик по имени».
// Публичный каталог — requireUID не нужен, в отличие от handleLikes.
func (a *App) handleArtistTracks(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	tracks, err := c.ArtistTracks(r.Context(), r.PathValue("artistId"))
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handleAlbumTracks — то же для альбома.
func (a *App) handleAlbumTracks(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	tracks, err := c.AlbumTracks(r.Context(), r.PathValue("albumId"))
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}
```

- [ ] **Step 4: Убедиться, что проходит (GREEN)**

Run: `go test ./internal/httpapi/... -run TestArtistAndAlbumTracksRoutes -v`
Expected: PASS

- [ ] **Step 5: Прогнать весь бэкенд и закоммитить**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -10`
Expected: `Success`, `No issues found`, все тесты проходят.

```bash
git add internal/httpapi/routes.go internal/httpapi/artist_album_test.go
git commit -m "feat(httpapi): роуты /api/artists/{id}/tracks и /api/albums/{id}/tracks"
```

---

## Task 5: Фронтенд — модель Track и кликабельные имена

**Files:**
- Modify: `web/src/app.ts` (`interface Track`, `render()` — шапка и строки очереди)
- Modify: `web/index.html` (новый `<div id="album">`, CSS `.entityLink`)

**Interfaces:**
- Consumes: `player.Track.ArtistIDs`/`AlbumID` (Task 1, приходят в SSE-кадре `State.track`/`State.queue[]`).
- Produces: `function entityLink(kind: "artists" | "albums", id: string, text: string): HTMLSpanElement` — используется в Task 6 для открытия карточки.

- [ ] **Step 1: Добавить поля в интерфейс Track**

В `web/src/app.ts`, `interface Track` (строка 11):

```ts
interface Track {
  id: string; title: string; artists: string[]; artistIds: string[];
  album: string; albumId: string; coverUrl: string; duration: number; available: boolean; liked: boolean;
}
```

- [ ] **Step 2: Добавить #album в разметку и CSS для кликабельных имён**

В `web/index.html`, после `<div id="artist"></div>` (строка 869):

```html
          <div id="artist"></div>
          <div id="album"></div>
```

В CSS рядом с `#artist { color:var(--dim); font-size:16px; margin-bottom:20px; }` (строка 136) — сузить нижний отступ у `#artist` и добавить `#album`:

```css
    #artist { color:var(--dim); font-size:16px; margin-bottom:2px; }
    #album { color:var(--faint); font-size:13px; margin-bottom:20px; }
    .entityLink { cursor:pointer; }
    .entityLink:hover { text-decoration:underline; }
```

- [ ] **Step 3: Функция entityLink и рендер шапки**

В `web/src/app.ts`, рядом с другими вспомогательными функциями (например, перед `render()`):

```ts
// entityLink — кликабельное имя артиста/альбома. stopPropagation обязателен
// для строк очереди: там весь <li> уже несёт свой click (переход по
// индексу очереди), и клик по имени внутри не должен запускать оба.
function entityLink(kind: "artists" | "albums", id: string, text: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "entityLink";
  span.textContent = text;
  span.addEventListener("click", (e) => {
    e.stopPropagation();
    openEntityCard(kind, id, text).catch(() => {});
  });
  return span;
}
```

Заменить в `render()` (строки 388-389):

```ts
  $("title").textContent = t ? t.title : "—";
  $("artist").textContent = t ? (t.artists ?? []).join(", ") : "";
```

на:

```ts
  $("title").textContent = t ? t.title : "—";
  const artistWrap = $("artist");
  artistWrap.innerHTML = "";
  (t?.artists ?? []).forEach((name, i) => {
    if (i > 0) artistWrap.append(", ");
    const id = t?.artistIds?.[i];
    artistWrap.append(id ? entityLink("artists", id, name) : document.createTextNode(name));
  });
  const albumWrap = $("album");
  albumWrap.innerHTML = "";
  if (t?.album) {
    albumWrap.append(t.albumId ? entityLink("albums", t.albumId, t.album) : document.createTextNode(t.album));
  }
```

- [ ] **Step 4: Кликабельные имена в строках очереди**

В `render()`, блок `queue.forEach` (строки 457-459) — заменить:

```ts
    const artistEl = document.createElement("span");
    artistEl.className = "a";
    artistEl.textContent = " — " + (track.artists ?? []).join(", ");
```

на:

```ts
    const artistEl = document.createElement("span");
    artistEl.className = "a";
    artistEl.append(" — ");
    (track.artists ?? []).forEach((name, ai) => {
      if (ai > 0) artistEl.append(", ");
      const id = track.artistIds?.[ai];
      artistEl.append(id ? entityLink("artists", id, name) : document.createTextNode(name));
    });
```

- [ ] **Step 5: Typecheck (компилятор — единственная автоматическая проверка фронтенда)**

Run (из каталога `web/`): `npm run typecheck`
Expected: без ошибок. Ожидаемая ошибка на этом шаге — `openEntityCard` ещё не объявлена (Task 6); если typecheck падает именно на этом — нормально, доделываем в Task 6 до коммита. Если падает на чём-то другом (опечатка в новых блоках) — исправить здесь.

- [ ] **Step 6: Коммит вместе с Task 6**

Этот шаг не коммитится отдельно: `entityLink` ссылается на `openEntityCard` из Task 6, typecheck всего файла пройдёт только после него. Коммит — в конце Task 6.

---

## Task 6: Фронтенд — карточка артиста/альбома

**Files:**
- Modify: `web/src/app.ts` (`openEntityCard`, `showQueue`, обработчик `btnLists`)
- Modify: `web/index.html` (`#cardHeader`/`#cardTitle`/`#cardBack`, CSS)

**Interfaces:**
- Consumes: `entityLink` (Task 5), `GET /api/artists/{id}/tracks`/`GET /api/albums/{id}/tracks` (Task 4), существующие `fetch`/`errorText`/`showTransientError`/`api`/`listEl`/`listsEl`/`fmtTime`.
- Produces: `function openEntityCard(kind: "artists" | "albums", id: string, title: string): Promise<void>`.

- [ ] **Step 1: Разметка и стили заголовка карточки**

В `web/index.html`, перед `<ul id="lists" class="hidden"></ul>` (строка 909):

```html
      <div id="cardHeader" class="hidden">
        <span id="cardTitle"></span>
        <button id="cardBack">✕ назад</button>
      </div>
      <ul id="lists" class="hidden"></ul>
```

CSS рядом с `.entityLink` (добавленным в Task 5):

```css
    #cardHeader { display:flex; align-items:center; justify-content:space-between; margin:0 0 10px; }
    #cardTitle { font-weight:700; font-size:15px; }
    #cardBack {
      background:none; border:none; padding:4px 8px;
      color:var(--dim); font-size:13px; cursor:pointer;
      transition:color .2s var(--ease);
    }
    #cardBack:hover { color:var(--fg); }
```

- [ ] **Step 2: openEntityCard и обновление showQueue**

В `web/src/app.ts` — заменить `showQueue` (строки 618-621):

```ts
function showQueue(): void {
  listsEl.classList.add("hidden");
  listEl.classList.remove("hidden");
}
```

на:

```ts
function showQueue(): void {
  listsEl.classList.add("hidden");
  $("cardHeader").classList.add("hidden");
  listEl.classList.remove("hidden");
}

// openEntityCard — карточка артиста/альбома поверх той же панели #lists,
// что уже рисует список плейлистов. Открытие карточки не трогает
// воспроизведение: очередь меняется только явным кликом по треку внутри.
async function openEntityCard(kind: "artists" | "albums", id: string, title: string): Promise<void> {
  const res = await fetch(`/api/${kind}/${id}/tracks`);
  if (!res.ok) { showTransientError(await errorText(res)); return; }
  const tracks = await res.json() as Track[];
  listsEl.innerHTML = "";
  tracks.forEach((track, i) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    const titleEl = document.createElement("span");
    titleEl.className = "t";
    titleEl.textContent = track.title;
    name.append(titleEl);
    const dur = document.createElement("span");
    dur.className = "dur";
    dur.textContent = fmtTime(track.duration);
    li.append(name, dur);
    li.addEventListener("click", () => {
      // Список от кликнутого трека и до конца — воспроизведение
      // продолжается дальше по карточке в том же порядке.
      api("/api/play", { source: "tracks", tracks: tracks.slice(i) }).catch(() => {});
      showQueue();
    });
    listsEl.appendChild(li);
  });
  $("cardTitle").textContent = title;
  $("cardHeader").classList.remove("hidden");
  listEl.classList.add("hidden");
  listsEl.classList.remove("hidden");
}

$("cardBack").addEventListener("click", showQueue);
```

- [ ] **Step 3: Спрятать шапку карточки при открытии панели плейлистов**

В обработчике `$("btnLists").addEventListener("click", ...)`, сразу после строки `if (!listsEl.classList.contains("hidden")) { showQueue(); return; }` изменений не требуется — этот же `return` уже уводит в `showQueue()`, которая теперь прячет и `#cardHeader`. Отдельный шаг: перед `listsEl.innerHTML = ""` в этом обработчике добавить `$("cardHeader").classList.add("hidden");` — защита от случая, когда панель открыта картой, а не плейлистами, и защита сработать не успела:

```ts
  const res = await fetch("/api/playlists");
  if (!res.ok) { showTransientError(await errorText(res)); return; }
  const lists = await res.json();
  $("cardHeader").classList.add("hidden");
  listsEl.innerHTML = "";
```

- [ ] **Step 4: Typecheck и сборка**

Run (из `web/`): `npm run typecheck && npm run build`
Expected: без ошибок; `esbuild` печатает размер бандла.

- [ ] **Step 5: Прогнать весь бэкенд ещё раз (модель Track менялась в Task 1, но лишний раз убедиться дёшево)**

Run: `go build ./... && go test ./... 2>&1 | tail -10`
Expected: `Success`, все тесты проходят.

- [ ] **Step 6: Коммит (Task 5 + Task 6 вместе — Task 5 не компилировался отдельно)**

```bash
git add web/src/app.ts web/index.html
git commit -m "feat(web): карточка артиста/альбома по клику на имя"
```

---

## Task 7: Ручная проверка в браузере

**Files:** нет изменений — только проверка уже реализованного.

- [ ] **Step 1: Запустить демон**

Run: `go run ./cmd/musicd -addr 127.0.0.1:8099 -no-open` (в фоне)

- [ ] **Step 2: Открыть в браузере и запустить воспроизведение**

Через claude-in-chrome: `navigate` на `http://127.0.0.1:8099`, дождаться загрузки очереди, запустить трек.

- [ ] **Step 3: Проверить карточку артиста из шапки**

Клик по имени артиста в шапке текущего трека → открывается `#lists` с заголовком `#cardTitle` = имя артиста и списком его треков; `#list` (очередь) скрыт; текущее воспроизведение не прервалось (проверить, что `#btnPlay` не переключился на паузу, время не сбросилось).

- [ ] **Step 4: Проверить клик по треку внутри карточки**

Клик по любому треку в открытой карточке → очередь заменяется треками артиста начиная с кликнутого, начинает играть с позиции 0, вид переключается обратно на `#list` (`showQueue()`).

- [ ] **Step 5: Проверить карточку альбома и кнопку «✕ назад»**

Открыть карточку альбома (клик по названию альбома в шапке) → тот же список, но из треков альбома. Нажать «✕ назад» без клика по треку → возврат к `#list`, воспроизведение не прервалось.

- [ ] **Step 6: Проверить кликабельные имена в строке очереди**

В списке очереди (`#list`) кликнуть по имени артиста внутри строки трека (не по самой строке) → открывается карточка артиста, а НЕ срабатывает переход по индексу очереди (`queue-index`) — это и проверяет `stopPropagation()` из Task 5.

- [ ] **Step 7: Проверить консоль на ошибки**

Через claude-in-chrome: `read_console_messages` с `onlyErrors: true` за всё время шагов 2-6.
Expected: пусто.

- [ ] **Step 8: Погасить демон**

Run: `lsof -ti:8099 | xargs -r kill`
