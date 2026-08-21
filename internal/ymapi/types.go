package ymapi

import (
	"strconv"
	"strings"

	"music212/internal/player"
)

// apiTrack — форма трека в ответах API. Поле ID приходит то строкой, то числом,
// поэтому разбирается как json.Number-совместимая строка.
type apiTrack struct {
	ID         any    `json:"id"`
	Title      string `json:"title"`
	Available  bool   `json:"available"`
	DurationMs int    `json:"durationMs"`
	CoverURI   string `json:"coverUri"`
	Artists    []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Albums []struct {
		Title string `json:"title"`
	} `json:"albums"`
}

// toPlayer переводит форму API во внутреннюю модель.
func (t apiTrack) toPlayer() player.Track {
	artists := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		artists = append(artists, a.Name)
	}
	album := ""
	if len(t.Albums) > 0 {
		album = t.Albums[0].Title
	}
	return player.Track{
		ID:        idString(t.ID),
		Title:     t.Title,
		Artists:   artists,
		Album:     album,
		CoverURL:  coverURL(t.CoverURI),
		Duration:  t.DurationMs / 1000,
		Available: t.Available,
	}
}

// idString нормализует идентификатор, который API отдаёт то строкой, то числом.
func idString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return ""
	}
}

// coverURL достраивает шаблон обложки до конкретного размера.
// API отдаёт путь с плейсхолдером %%, например "avatars.yandex.net/get-music-content/1/%%".
func coverURL(uri string) string {
	if uri == "" {
		return ""
	}
	return "https://" + strings.Replace(uri, "%%", "400x400", 1)
}
