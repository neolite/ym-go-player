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
