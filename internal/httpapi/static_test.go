package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// .gitkeep — единственный файл, гарантированно закоммиченный в
// internal/httpapi/dist на любом дереве (собранного фронтенда там может и
// не быть, если make web ещё не запускали). Раздача этого файла доказывает,
// что //go:embed all:dist и fs.Sub("dist") действительно работают, не
// завися от того, собран ли фронтенд.
func TestStaticHandler_ServesEmbeddedGitkeep(t *testing.T) {
	h := StaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/.gitkeep", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /.gitkeep = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestStaticHandler_UnknownPathIsNotFound(t *testing.T) {
	h := StaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist-anywhere.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /does-not-exist-anywhere.js = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
