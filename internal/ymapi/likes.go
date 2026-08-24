package ymapi

import (
	"context"
	"net/url"
	"strconv"
)

// LikeTrack добавляет трек в «Мне нравится» (POST add-multiple из
// исследования: docs/research/custom-yandex-music-player.md, строка
// users_likes_tracks_add). Проверено живым прогоном против боевого
// сервиса (2026-08): лайк проходит, ответ 200.
func (c *Client) LikeTrack(ctx context.Context, uid int64, trackID string) error {
	return c.setTrackLike(ctx, uid, trackID, "add-multiple")
}

// UnlikeTrack снимает лайк с трека. Путь — remove (без «-multiple»):
// remove-multiple не существует, боевой сервис отвечает на него 404
// not-found; по исходнику MarshalX/yandex-music-api снятие идёт на
// .../likes/tracks/remove.
func (c *Client) UnlikeTrack(ctx context.Context, uid int64, trackID string) error {
	return c.setTrackLike(ctx, uid, trackID, "remove")
}

func (c *Client) setTrackLike(ctx context.Context, uid int64, trackID, op string) error {
	form := url.Values{"track-ids": {trackID}}
	path := "/users/" + strconv.FormatInt(uid, 10) + "/likes/tracks/" + op
	return c.PostForm(ctx, path, form, nil)
}

// LikedTrackIDs отдаёт идентификаторы лайкнутых треков без похода за
// метаданными (в отличие от LikedTracks, которая резолвит их через /tracks).
func (c *Client) LikedTrackIDs(ctx context.Context, uid int64) ([]string, error) {
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
	return ids, nil
}
