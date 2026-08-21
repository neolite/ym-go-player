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
