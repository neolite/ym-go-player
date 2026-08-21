package ymapi

import (
	"context"
	"net/url"

	"music212/internal/player"
)

// WaveStationID — идентификатор станции «Моя волна».
const WaveStationID = "user:onyourwave"

// WaveBatch — очередная порция треков от ротора.
// BatchID обязателен при отправке фидбека: без него волна не понимает,
// к какой выдаче относится событие.
type WaveBatch struct {
	BatchID string
	Tracks  []player.Track
}

// StationTracks запрашивает следующий батч станции.
// lastTrackID сообщает ротору, чем закончилась предыдущая порция;
// при первом обращении передаётся пустая строка.
func (c *Client) StationTracks(ctx context.Context, station, lastTrackID string) (*WaveBatch, error) {
	q := url.Values{"settings2": {"true"}}
	if lastTrackID != "" {
		q.Set("queue", lastTrackID)
	}
	var res struct {
		BatchID  string `json:"batchId"`
		Sequence []struct {
			Track apiTrack `json:"track"`
		} `json:"sequence"`
	}
	if err := c.Get(ctx, "/rotor/station/"+station+"/tracks", q, &res); err != nil {
		return nil, err
	}
	tracks := make([]player.Track, 0, len(res.Sequence))
	for _, s := range res.Sequence {
		tracks = append(tracks, s.Track.toPlayer())
	}
	return &WaveBatch{BatchID: res.BatchID, Tracks: tracks}, nil
}
