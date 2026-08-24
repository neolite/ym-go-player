package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"music212/internal/auth"
	"music212/internal/player"
	"music212/internal/ymapi"
)

// newLikedIDsBackend имитирует только «Мне нравится» — GET
// /users/{uid}/likes/tracks, форма ответа как в library_test.go likesFixture.
func newLikedIDsBackend(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitLikedLoaded(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		app.mu.RLock()
		loaded := app.likedLoaded
		app.mu.RUnlock()
		if loaded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("кэш лайков не загрузился за 2 секунды")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// GET /api/events обязан лениво подгрузить набор лайкнутых ID и отметить им
// текущий трек в снимке состояния.
func TestSnapshotMarksCurrentTrackLikedFromCache(t *testing.T) {
	srv := newLikedIDsBackend(t, `{"result":{"library":{"tracks":[{"id":"42"},{"id":"43"}]}}}`)

	q := player.NewQueue()
	q.Set([]player.Track{{Available: true, ID: "42", Duration: 200}}, "tracks")
	app := &App{
		Auth:  NewAuth(auth.NewMemory(), okVerify),
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	mux := app.Routes()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"good"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if got := app.snapshot().Track; got == nil || got.Liked {
		t.Fatalf("до загрузки кэша Liked = %+v, want false (кэш ещё пуст)", got)
	}

	app.ensureLikedLoaded()
	waitLikedLoaded(t, app)

	st := app.snapshot()
	if st.Track == nil || !st.Track.Liked {
		t.Fatalf("после загрузки кэша Track = %+v, want Liked=true", st.Track)
	}
}

// Трек вне набора лайков должен остаться Liked=false.
func TestSnapshotLeavesUnlikedTrackFalse(t *testing.T) {
	srv := newLikedIDsBackend(t, `{"result":{"library":{"tracks":[{"id":"42"}]}}}`)

	q := player.NewQueue()
	q.Set([]player.Track{{Available: true, ID: "99", Duration: 200}}, "tracks")
	app := &App{
		Auth:  NewAuth(auth.NewMemory(), okVerify),
		Queue: q,
		Hub:   NewHub(),
		Client: func() (*ymapi.Client, error) {
			return ymapi.NewWithBase("t", srv.URL), nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"good"}`))
	req.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(httptest.NewRecorder(), req)

	app.ensureLikedLoaded()
	waitLikedLoaded(t, app)

	st := app.snapshot()
	if st.Track == nil || st.Track.Liked {
		t.Fatalf("Track = %+v, want Liked=false", st.Track)
	}
}

// Лайк/дизлайк обязаны обновлять кэш немедленно, не дожидаясь фонового
// перечитывания /likes/tracks — иначе сердечко не отразит только что
// нажатый лайк до следующего события ensureLikedLoaded.
func TestLikeAndUnlikeUpdateCacheImmediately(t *testing.T) {
	srv, _, _ := newLikeFakeBackend(t)
	defer srv.Close()

	app, mux := authedWaveApp(t, srv, []player.Track{{Available: true, ID: "a", Duration: 200}})

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/tracks/a/like", nil))
	if st := app.snapshot(); st.Track == nil || !st.Track.Liked {
		t.Fatalf("после лайка Track = %+v, want Liked=true", st.Track)
	}

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/tracks/a/like", nil))
	if st := app.snapshot(); st.Track == nil || st.Track.Liked {
		t.Fatalf("после снятия лайка Track = %+v, want Liked=false", st.Track)
	}
}
