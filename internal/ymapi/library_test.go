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

const emptyLikesFixture = `{"result":{"library":{"tracks":[]}}}`

func TestSearchTracks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
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
	// coverURL("cdn/%%") подставляет размер вместо плейсхолдера %% и
	// добавляет схему — см. TestCoverURL в types_test.go. Значение записано
	// константой, а не вычислено вызовом coverURL, иначе тест сравнивал бы
	// функцию саму с собой и не смог бы упасть на опечатке в теге "cover".
	const wantCoverURL = "https://cdn/400x400"
	if got[0].CoverURL != wantCoverURL {
		t.Fatalf("CoverURL = %q, want %q", got[0].CoverURL, wantCoverURL)
	}
}

func TestPlaylistTracksUnwrapsNesting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/555/playlists/1001" {
			t.Errorf("path = %q, want /users/555/playlists/1001", r.URL.Path)
		}
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
// Проверяются оба запроса: путь и метод лайков, и метод/путь запроса метаданных —
// смысл теста в том, что вызовов ровно два и они разные.
func TestLikedTracksResolvesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tracks" {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST для /tracks", r.Method)
			}
			w.Write([]byte(tracksFixture))
			return
		}
		if r.URL.Path != "/users/555/likes/tracks" {
			t.Errorf("path = %q, want /users/555/likes/tracks", r.URL.Path)
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

// Пустая библиотека лайков не должна дёргать /tracks — Tracks на пустом
// списке идентификаторов возвращает nil, nil, не обращаясь к API (задача 6).
func TestLikedTracksOnEmptyLibrary(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/users/555/likes/tracks" {
			t.Errorf("path = %q, want /users/555/likes/tracks", r.URL.Path)
		}
		w.Write([]byte(emptyLikesFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).LikedTracks(context.Background(), 555)
	if err != nil {
		t.Fatalf("LikedTracks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("треки = %+v, want пустой список", got)
	}
	if calls != 1 {
		t.Fatalf("обращений к серверу = %d, want 1 (только за лайками)", calls)
	}
}
