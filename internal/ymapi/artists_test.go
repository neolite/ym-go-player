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
