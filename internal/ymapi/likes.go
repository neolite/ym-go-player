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
