package ymapi

import (
	"context"
	"strconv"

	"music212/internal/player"
)

// PlaylistInfo — карточка плейлиста для списка в библиотеке.
type PlaylistInfo struct {
	Kind       int    `json:"kind"`
	Title      string `json:"title"`
	TrackCount int    `json:"trackCount"`
	CoverURL   string `json:"coverUrl"`
}

// UserPlaylists перечисляет плейлисты пользователя.
func (c *Client) UserPlaylists(ctx context.Context, uid int64) ([]PlaylistInfo, error) {
	var res []struct {
		Kind       int    `json:"kind"`
		Title      string `json:"title"`
		TrackCount int    `json:"trackCount"`
		Cover      struct {
			URI string `json:"uri"`
		} `json:"cover"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/playlists/list"
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	out := make([]PlaylistInfo, 0, len(res))
	for _, p := range res {
		out = append(out, PlaylistInfo{
			Kind:       p.Kind,
			Title:      p.Title,
			TrackCount: p.TrackCount,
			CoverURL:   coverURL(p.Cover.URI),
		})
	}
	return out, nil
}

// PlaylistTracks возвращает содержимое плейлиста.
// API оборачивает каждый трек в объект с полем track — разворачиваем.
func (c *Client) PlaylistTracks(ctx context.Context, uid int64, kind int) ([]player.Track, error) {
	var res struct {
		Tracks []struct {
			Track apiTrack `json:"track"`
		} `json:"tracks"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/playlists/" + strconv.Itoa(kind)
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res.Tracks))
	for _, w := range res.Tracks {
		out = append(out, w.Track.toPlayer())
	}
	return out, nil
}

// LikedTracks возвращает «Мне нравится».
// Эндпоинт отдаёт только идентификаторы, поэтому метаданные добираются
// вторым запросом через /tracks.
func (c *Client) LikedTracks(ctx context.Context, uid int64) ([]player.Track, error) {
	var res struct {
		Library struct {
			Tracks []struct {
				ID any `json:"id"`
			} `json:"tracks"`
		} `json:"library"`
	}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/likes/tracks"
	if err := c.Get(ctx, path, nil, &res); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Library.Tracks))
	for _, t := range res.Library.Tracks {
		if id := idString(t.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return c.Tracks(ctx, ids)
}
