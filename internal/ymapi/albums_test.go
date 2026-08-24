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
