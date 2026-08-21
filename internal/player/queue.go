package player

import "sync"

// Queue — позиция в списке треков. Все методы безопасны для конкурентного вызова.
type Queue struct {
	mu     sync.RWMutex
	tracks []Track
	index  int
	source string
}

// NewQueue создаёт пустую очередь.
func NewQueue() *Queue { return &Queue{} }

// Set заменяет очередь целиком и встаёт на первый трек.
func (q *Queue) Set(tracks []Track, source string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = normalizeTracks(tracks)
	q.index = 0
	q.source = source
}

// Append дозаливает треки в конец, не трогая текущую позицию.
// Так работает ротор: он подкачивает батчи прямо во время воспроизведения.
func (q *Queue) Append(tracks []Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = append(q.tracks, normalizeTracks(tracks)...)
}

// normalizeTracks гарантирует, что срезовые поля треков не станут nil.
// encoding/json кодирует nil-срез как null, а не [], а Track.Artists может
// прийти нормализованным как угодно — в том числе nil, если source:"tracks"
// в POST /api/play принёс объект без поля "artists" из клиентского JSON,
// где никакой toPlayer()-конвертации, гарантирующей непустой срез, не было.
// Точка входа в очередь — то единственное место в internal/player, где это
// можно поймать для любого трека, независимо от того, как он был собран.
func normalizeTracks(tracks []Track) []Track {
	out := make([]Track, len(tracks))
	for i, t := range tracks {
		if t.Artists == nil {
			t.Artists = []string{}
		}
		out[i] = t
	}
	return out
}

// Current возвращает текущий трек либо nil, если очередь пуста.
func (q *Queue) Current() *Track {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.index < 0 || q.index >= len(q.tracks) {
		return nil
	}
	t := q.tracks[q.index]
	return &t
}

// Next сдвигает позицию вперёд. false означает конец очереди.
func (q *Queue) Next() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.index+1 >= len(q.tracks) {
		return false
	}
	q.index++
	return true
}

// Prev сдвигает позицию назад. false означает начало очереди.
func (q *Queue) Prev() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.index <= 0 {
		return false
	}
	q.index--
	return true
}

// Remaining — сколько треков осталось после текущего.
// Ротор использует это, чтобы решить, пора ли подкачивать батч.
func (q *Queue) Remaining() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	n := len(q.tracks) - q.index - 1
	if n < 0 {
		return 0
	}
	return n
}

// Snapshot отдаёт копию очереди, позицию и источник.
//
// Копия обязана быть non-nil, даже когда q.tracks пуст или ещё ни разу не
// присваивался (до первого Set — например, самый первый кадр SSE в
// состоянии простоя): append([]Track(nil), ...) на пустом источнике
// возвращает nil, а encoding/json кодирует nil-срез как null, а не []. Это
// контракт HTTP-интерфейса (см. State.Queue в state.go) — любой потребитель
// кадра, включая веб-клиент, обязан получать здесь настоящий пустой массив.
func (q *Queue) Snapshot() ([]Track, int, string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]Track, len(q.tracks))
	copy(out, q.tracks)
	return out, q.index, q.source
}
