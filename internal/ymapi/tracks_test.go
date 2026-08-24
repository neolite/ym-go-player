package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const tracksFixture = `{"result":[{
  "id":"12345","title":"Тестовый трек","available":true,"durationMs":183000,
  "coverUri":"avatars.example.net/get-music-content/1/%%",
  "artists":[{"id":111,"name":"Первый"},{"id":222,"name":"Второй"}],
  "albums":[{"id":333,"title":"Альбом"}]
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
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/tracks" {
			t.Errorf("path = %s, want /tracks", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("trackIds"); got != "12345" {
			t.Errorf("trackIds = %q, want %q", got, "12345")
		}
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
	if len(tr.ArtistIDs) != 2 || tr.ArtistIDs[0] != "111" || tr.ArtistIDs[1] != "222" {
		t.Fatalf("artistIDs = %v", tr.ArtistIDs)
	}
	if tr.AlbumID != "333" {
		t.Fatalf("albumID = %q, want 333", tr.AlbumID)
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

// TestDownloadVariantsParsesFixture проверяет разбор ответа download-info:
// анонимная структура с JSON-тегами в DownloadVariants не покрыта ничем,
// кроме этого теста, а опечатка в любом теге молча даёт нулевые/пустые поля.
func TestDownloadVariantsParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/tracks/12345/download-info" {
			t.Errorf("path = %s, want /tracks/12345/download-info", r.URL.Path)
		}
		w.Write([]byte(downloadInfoFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).DownloadVariants(context.Background(), "12345")
	if err != nil {
		t.Fatalf("DownloadVariants: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	first := got[0]
	if first.Codec != "mp3" || first.BitrateKbps != 192 || !strings.HasSuffix(first.InfoURL, "/dl/192") || first.Preview {
		t.Fatalf("first = %+v", first)
	}
	third := got[2]
	if third.Codec != "aac" || third.BitrateKbps != 64 || !third.Preview {
		t.Fatalf("third = %+v", third)
	}
}

// Ключевой тест шагающего скелета: download-info -> XML -> подписанная ссылка.
func TestResolveDirectLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
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

// TestResolveDirectLinkUsesMetadataClient закрепляет выбор HTTP-клиента внутри
// ResolveDirectLink. XML download-info — маленький запрос метаданных, поэтому
// он обязан идти через c.http (общий Timeout), а не через c.stream (у него
// общего таймаута нет — только ResponseHeaderTimeout). Здесь укорачивается
// именно c.http.Timeout: если код по ошибке переключат на c.stream, короткий
// таймаут перестанет действовать, сервер успеет ответить за отведённые ему
// 300 мс до срабатывания ResponseHeaderTimeout: 20s, и тест перестанет
// падать. 20 мс здесь — не флаки-таймаут, а сознательно выбранное значение,
// заведомо меньшее искусственной задержки хендлера (300 мс); не увеличивать.
func TestResolveDirectLinkUsesMetadataClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(downloadXMLFixture))
	}))
	defer srv.Close()

	c := NewWithBase("t", srv.URL)
	c.http.Timeout = 20 * time.Millisecond

	_, err := c.ResolveDirectLink(context.Background(), DownloadVariant{InfoURL: srv.URL + "/dl"})
	if err == nil {
		t.Fatal("ResolveDirectLink: ожидалась ошибка таймаута c.http, получили nil")
	}
}

// TestResolveDirectLinkRejectsIncompleteXML закрепляет guard на tracks.go:127:
// без host/path DirectLinkMP3 соберёт синтаксически валидную, но бессмысленную
// ссылку, и запрос по ней упадёт позже, где исходную причину не восстановить
// (паники здесь нет — SignMP3 сама устойчива к пустому path). Локальные
// XML-строки — намеренно не downloadXMLFixture, чтобы не трогать константу,
// на которую опираются тесты будущих задач.
func TestResolveDirectLinkRejectsIncompleteXML(t *testing.T) {
	cases := []struct {
		name string
		xml  string
	}{
		{
			name: "пустой path",
			xml: `<?xml version="1.0" encoding="utf-8"?>
<download-info>
  <host>s1.example.net</host>
  <path></path>
  <ts>1700000000</ts>
  <s>somesalt</s>
</download-info>`,
		},
		{
			name: "пустой host",
			xml: `<?xml version="1.0" encoding="utf-8"?>
<download-info>
  <host></host>
  <path>/abc/def.mp3</path>
  <ts>1700000000</ts>
  <s>somesalt</s>
</download-info>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.Write([]byte(tc.xml))
			}))
			defer srv.Close()

			c := NewWithBase("t", srv.URL)
			_, err := c.ResolveDirectLink(context.Background(), DownloadVariant{InfoURL: srv.URL + "/dl"})
			if err == nil {
				t.Fatal("ResolveDirectLink: ожидалась ошибка на неполном XML")
			}
		})
	}
}
