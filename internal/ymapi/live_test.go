//go:build live

// Живой смоук против настоящего API. Не запускается в обычном прогоне.
// Запуск: YM_TOKEN=<токен> go test -tags live ./internal/ymapi/ -run TestLive -v
package ymapi

import (
	"context"
	"os"
	"testing"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	token := os.Getenv("YM_TOKEN")
	if token == "" {
		t.Skip("YM_TOKEN не задан")
	}
	return New(token)
}

func TestLiveAccountStatus(t *testing.T) {
	st, err := liveClient(t).AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	t.Logf("аккаунт: login=%s region=%d plus=%v", st.Login, st.Region, st.HasPlus)
	if !st.HasPlus {
		t.Fatal("нет подписки Плюс — воспроизведение работать не будет")
	}
}

// Замыкает шагающий скелет: волна -> трек -> подписанная ссылка.
func TestLiveWalkingSkeleton(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	batch, err := c.StationTracks(ctx, WaveStationID, "")
	if err != nil {
		t.Fatalf("StationTracks: %v", err)
	}
	if len(batch.Tracks) == 0 {
		t.Fatal("волна вернула пустой батч")
	}
	track := batch.Tracks[0]
	t.Logf("трек: %s — %v", track.Title, track.Artists)

	variants, err := c.DownloadVariants(ctx, track.ID)
	if err != nil {
		t.Fatalf("DownloadVariants: %v", err)
	}
	for _, v := range variants {
		t.Logf("вариант: codec=%s bitrate=%d preview=%v", v.Codec, v.BitrateKbps, v.Preview)
	}

	link, err := c.ResolveTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("ResolveTrack: %v", err)
	}
	if len(link) < 20 {
		t.Fatalf("подозрительная ссылка: %q", link)
	}
	t.Log("шагающий скелет замкнулся: подписанная ссылка получена")
}
