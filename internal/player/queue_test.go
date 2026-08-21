package player

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func tracks(ids ...string) []Track {
	out := make([]Track, 0, len(ids))
	for _, id := range ids {
		out = append(out, Track{ID: id, Available: true})
	}
	return out
}

func TestEmptyQueueHasNoCurrent(t *testing.T) {
	q := NewQueue()
	if q.Current() != nil {
		t.Fatal("Current на пустой очереди должен быть nil")
	}
	if q.Next() {
		t.Fatal("Next на пустой очереди должен вернуть false")
	}
}

func TestSetStartsAtFirstTrack(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b", "c"), "playlist")
	if got := q.Current(); got == nil || got.ID != "a" {
		t.Fatalf("Current = %v, want a", got)
	}
}

func TestNextAdvancesAndStopsAtEnd(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "playlist")

	if !q.Next() || q.Current().ID != "b" {
		t.Fatal("Next должен перейти на b")
	}
	if q.Next() {
		t.Fatal("Next на последнем треке должен вернуть false")
	}
	if q.Current().ID != "b" {
		t.Fatal("после отказа Next текущий трек не должен меняться")
	}
}

func TestPrevStopsAtStart(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "playlist")
	if q.Prev() {
		t.Fatal("Prev на первом треке должен вернуть false")
	}
	q.Next()
	if !q.Prev() || q.Current().ID != "a" {
		t.Fatal("Prev должен вернуть на a")
	}
}

// Ротор дозаливает треки на ходу — позиция при этом обязана сохраняться.
func TestAppendKeepsPosition(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b"), "wave")
	q.Next()
	q.Append(tracks("c", "d"))

	if q.Current().ID != "b" {
		t.Fatalf("Append сдвинул позицию: Current = %s", q.Current().ID)
	}
	if got := q.Remaining(); got != 2 {
		t.Fatalf("Remaining = %d, want 2", got)
	}
}

func TestRemainingCountsTracksAfterCurrent(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b", "c"), "wave")
	if got := q.Remaining(); got != 2 {
		t.Fatalf("Remaining = %d, want 2", got)
	}
}

func TestSnapshotReturnsSource(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a"), "wave")
	_, idx, src := q.Snapshot()
	if idx != 0 || src != "wave" {
		t.Fatalf("Snapshot = (%d, %q), want (0, \"wave\")", idx, src)
	}
}

// На пустой очереди (в том числе изначальной, до первого Set) Snapshot
// обязан сериализоваться как []Track{} → "[]" в JSON, а не как null:
// именно это ловит демон в первом кадре SSE в состоянии простоя.
// Проверяем сериализованный JSON, а не len(...) == 0 — это доказывает
// свойство, len() проходит и на nil-срезе тоже.
func TestSnapshotEmptyQueueSerializesAsEmptyArray(t *testing.T) {
	q := NewQueue()
	got, _, _ := q.Snapshot()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("Snapshot() пустой очереди сериализовался как %s, want []", raw)
	}
}

// Set/Append обязаны нормализовать Track.Artists так же, как Snapshot
// нормализует сам срез очереди: трек без артистов (nil-срез, например из
// клиентского JSON в source:"tracks", где поле artists не пришло) не
// должен превращать "artists" в null в исходящем кадре.
func TestSetNormalizesNilArtists(t *testing.T) {
	q := NewQueue()
	q.Set([]Track{{ID: "a", Title: "без артистов"}}, "tracks")
	got, _, _ := q.Snapshot()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"artists":null`) {
		t.Fatalf(`Snapshot содержит "artists":null: %s`, raw)
	}
	if !strings.Contains(string(raw), `"artists":[]`) {
		t.Fatalf(`Snapshot не содержит "artists":[]: %s`, raw)
	}
}

func TestAppendNormalizesNilArtists(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a"), "wave")
	q.Append([]Track{{ID: "b", Title: "без артистов"}})
	got, _, _ := q.Snapshot()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"artists":null`) {
		t.Fatalf(`Snapshot содержит "artists":null: %s`, raw)
	}
}

// TestQueueIsSafeForConcurrentUse нагружает очередь конкурентно: SSE-хаб читает
// Snapshot в одной горутине, пока HTTP-обработчики дёргают Next/Prev/Append в других.
// Тест не проверяет конкретные значения, зависящие от порядка выполнения — только
// то, что гонок и паник нет (см. go test -race).
func TestQueueIsSafeForConcurrentUse(t *testing.T) {
	q := NewQueue()
	q.Set(tracks("a", "b", "c"), "wave")

	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (id + i) % 5 {
				case 0:
					_, _, _ = q.Snapshot()
				case 1:
					_ = q.Current()
				case 2:
					_ = q.Next()
				case 3:
					_ = q.Prev()
				case 4:
					q.Append(tracks("x"))
					_ = q.Remaining()
				}
			}
		}(g)
	}

	wg.Wait()
}
