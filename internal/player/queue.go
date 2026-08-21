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
	q.tracks = append([]Track(nil), tracks...)
	q.index = 0
	q.source = source
}

// Append дозаливает треки в конец, не трогая текущую позицию.
// Так работает ротор: он подкачивает батчи прямо во время воспроизведения.
func (q *Queue) Append(tracks []Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = append(q.tracks, tracks...)
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
func (q *Queue) Snapshot() ([]Track, int, string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return append([]Track(nil), q.tracks...), q.index, q.source
}
