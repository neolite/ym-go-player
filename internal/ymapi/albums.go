package ymapi

import (
	"context"

	"music212/internal/player"
)

// AlbumTracks отдаёт треки альбома для карточки, одним плоским списком.
// Yandex возвращает альбом дисками (volumes) — здесь они разворачиваются
// в один список, порядок дисков и треков внутри диска сохраняется
// (спека docs/superpowers/specs/2026-08-24-artist-album-navigation-design.md §5).
func (c *Client) AlbumTracks(ctx context.Context, albumID string) ([]player.Track, error) {
	var res struct {
		Volumes [][]apiTrack `json:"volumes"`
	}
	if err := c.Get(ctx, "/albums/"+albumID+"/with-tracks", nil, &res); err != nil {
		return nil, err
	}
	var out []player.Track
	for _, vol := range res.Volumes {
		for _, t := range vol {
			out = append(out, t.toPlayer())
		}
	}
	return out, nil
}
