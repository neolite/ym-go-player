package ymapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Лайк и снятие лайка обязаны уходить POST-формой на add-multiple / remove
// (именно remove, без «-multiple» — такого пути на сервисе нет, отвечает
// 404) с идентификатором трека в track-ids.
func TestLikeAndUnlikeTrack(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
		op   string
	}{
		{"like", func(c *Client) error { return c.LikeTrack(context.Background(), 555, "101") }, "add-multiple"},
		{"unlike", func(c *Client) error { return c.UnlikeTrack(context.Background(), 555, "101") }, "remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}
				want := "/users/555/likes/tracks/" + tc.op
				if r.URL.Path != want {
					t.Errorf("path = %q, want %q", r.URL.Path, want)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				if got := r.PostForm.Get("track-ids"); got != "101" {
					t.Errorf("track-ids = %q, want 101", got)
				}
				w.Write([]byte(`{"result":"ok"}`))
			}))
			defer srv.Close()

			if err := tc.call(NewWithBase("t", srv.URL)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}

// Лайк и дизлайк обязаны нести totalPlayedSeconds — ротору важно, на какой
// секунде трек оценили (в отличие от trackStarted, где поля быть не должно).
func TestRotorFeedbackLikeCarriesPlayedSeconds(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("разбор тела: %v", err)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", EventDislike, "101", 47)
	if err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if body["type"] != "dislike" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["totalPlayedSeconds"] != float64(47) {
		t.Fatalf("totalPlayedSeconds = %v, want 47", body["totalPlayedSeconds"])
	}
}
