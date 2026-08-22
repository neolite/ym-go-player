package ymapi

import (
	"context"
	"net/url"
	"strconv"
)

// Типы событий ротора.
const (
	EventRadioStarted  = "radioStarted"
	EventTrackStarted  = "trackStarted"
	EventTrackFinished = "trackFinished"
	EventSkip          = "skip"
	EventLike          = "like"
	EventDislike       = "dislike"
)

// RotorFeedback сообщает волне, что произошло с треком.
// Это канал обучения самой станции: без него выдача деградирует.
func (c *Client) RotorFeedback(ctx context.Context, station, batchID, eventType, trackID string, playedSeconds float64) error {
	body := map[string]any{
		"type":      eventType,
		"timestamp": nowUnixFloat(),
	}
	if trackID != "" {
		body["trackId"] = trackID
	}
	// totalPlayedSeconds имеет смысл только там, где прослушивание уже
	// завершилось или было прервано: у radioStarted и trackStarted ещё нет
	// «сколько проиграно», а лишнее поле ротор может воспринять как
	// противоречивое. Лайк и дизлайк тоже несут позицию — ротору важно,
	// на какой секунде трек оценили.
	switch eventType {
	case EventTrackFinished, EventSkip, EventLike, EventDislike:
		body["totalPlayedSeconds"] = playedSeconds
	}
	q := url.Values{}
	if batchID != "" {
		q.Set("batch-id", batchID)
	}
	return c.PostJSON(ctx, "/rotor/station/"+station+"/feedback", q, body, nil)
}

// PlayEvent — завершённое прослушивание для общей статистики аккаунта.
type PlayEvent struct {
	TrackID       string
	AlbumID       string
	From          string
	PlayedSeconds float64
	TotalSeconds  float64
	// UID — идентификатор пользователя, каким его отдаёт Auth.Status().UID.
	// Нулевое значение означает «не задан» и отправляется пустой строкой,
	// а не выдуманным "0".
	UID int64
	// PlayID — идентификатор конкретного прослушивания. Если не задан,
	// собирается автоматически из TrackID, AlbumID и метки времени, чтобы
	// быть уникальным на каждое прослушивание.
	PlayID string
}

// PlayAudio отправляет событие прослушивания.
// Это второй, независимый от ротора канал: он питает рекомендации аккаунта
// целиком, а не только волну.
//
// timestamp и client-now идут в формате ISO 8601 с миллисекундами
// (YYYY-MM-DDThh:mm:ss.SSSZ) — так подтверждает исходник MarshalX
// (yandex_music/_client_async/tracks.py, play_audio) и живой сервис:
// на unix-метку он отвечает 400 invalid-timestamp («Please use
// YYYY-MM-DDThh:mm:ss.SSSZ timestamp format»).
func (c *Client) PlayAudio(ctx context.Context, ev PlayEvent) error {
	uid := ""
	if ev.UID != 0 {
		uid = strconv.FormatInt(ev.UID, 10)
	}
	playID := ev.PlayID
	if playID == "" {
		playID = ev.TrackID + "_" + ev.AlbumID + "_" + formatFloat(nowUnixFloat())
	}
	now := nowISO8601()
	form := url.Values{
		"track-id":             {ev.TrackID},
		"album-id":             {ev.AlbumID},
		"from":                 {ev.From},
		"play-id":              {playID},
		"uid":                  {uid},
		"timestamp":            {now},
		"track-length-seconds": {formatFloat(ev.TotalSeconds)},
		"total-played-seconds": {formatFloat(ev.PlayedSeconds)},
		"end-position-seconds": {formatFloat(ev.PlayedSeconds)},
		"client-now":           {now},
	}
	return c.PostForm(ctx, "/play-audio", form, nil)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
