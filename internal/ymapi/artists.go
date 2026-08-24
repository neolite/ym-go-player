package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// ArtistTracks отдаёт треки артиста — карточка «клик по имени», не полный
// каталог: page-size фиксирован, пагинации на нашей стороне нет (спека
// docs/superpowers/specs/2026-08-24-artist-album-navigation-design.md §2/§5).
func (c *Client) ArtistTracks(ctx context.Context, artistID string) ([]player.Track, error) {
	q := url.Values{"page": {"0"}, "page-size": {"100"}}
	var res struct {
		Tracks []apiTrack `json:"tracks"`
	}
	if err := c.Get(ctx, "/artists/"+artistID+"/tracks", q, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks))
	for _, t := range res.Tracks {
		out = append(out, t.toPlayer())
	}
	return out, nil
}
