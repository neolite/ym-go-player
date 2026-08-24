// Package player хранит очередь и состояние воспроизведения.
// Модуль намеренно не делает сетевых вызовов — это делает его тестируемым без сети.
package player

// Status — стадия воспроизведения.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusLoading Status = "loading"
	StatusPlaying Status = "playing"
	StatusPaused  Status = "paused"
	StatusError   Status = "error"
)

// Track — минимум метаданных, нужный для отрисовки и воспроизведения.
type Track struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
	ArtistIDs []string `json:"artistIds"`
	Album     string   `json:"album"`
	AlbumID   string   `json:"albumId"`
	CoverURL  string   `json:"coverUrl"`
	Duration  int      `json:"duration"`
	Available bool     `json:"available"`
	Liked     bool     `json:"liked"`
}

// State — единственный источник правды о воспроизведении.
// Фронтенд получает его через SSE и своего состояния не хранит.
type State struct {
	Status     Status  `json:"status"`
	Track      *Track  `json:"track"`
	Position   float64 `json:"position"`
	Duration   float64 `json:"duration"`
	Volume     float64 `json:"volume"`
	Queue      []Track `json:"queue"`
	QueueIndex int     `json:"queueIndex"`
	Source     string  `json:"source"`
	Error      string  `json:"error,omitempty"`
}
