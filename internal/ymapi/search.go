package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// SearchTracks ищет треки по текстовому запросу.
// Забирается только первая страница результатов — для версии 1 этого
// достаточно, пагинация не реализована.
func (c *Client) SearchTracks(ctx context.Context, text string) ([]player.Track, error) {
	q := url.Values{
		"text":             {text},
		"type":             {"track"},
		"page":             {"0"},
		"nocorrect":        {"false"},
		"playlist-in-best": {"false"},
	}
	var res struct {
		Tracks struct {
			Results []apiTrack `json:"results"`
		} `json:"tracks"`
	}
	if err := c.Get(ctx, "/search", q, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks.Results))
	for _, t := range res.Tracks.Results {
		out = append(out, t.toPlayer())
	}
	return out, nil
}
