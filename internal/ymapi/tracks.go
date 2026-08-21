package ymapi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"music212/internal/player"
)

// DownloadVariant — один доступный вариант качества для трека.
type DownloadVariant struct {
	Codec       string
	BitrateKbps int
	InfoURL     string
	Preview     bool
}

// Tracks возвращает метаданные треков по идентификаторам.
func (c *Client) Tracks(ctx context.Context, ids []string) ([]player.Track, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	form := url.Values{"trackIds": {strings.Join(ids, ",")}}
	var res []apiTrack
	if err := c.PostForm(ctx, "/tracks", form, &res); err != nil {
		return nil, err
	}
	out := make([]player.Track, 0, len(res))
	for _, t := range res {
		out = append(out, t.toPlayer())
	}
	return out, nil
}

// DownloadVariants перечисляет доступные качества трека.
func (c *Client) DownloadVariants(ctx context.Context, trackID string) ([]DownloadVariant, error) {
	var res []struct {
		Codec           string `json:"codec"`
		BitrateInKbps   int    `json:"bitrateInKbps"`
		DownloadInfoURL string `json:"downloadInfoUrl"`
		Preview         bool   `json:"preview"`
	}
	if err := c.Get(ctx, "/tracks/"+trackID+"/download-info", nil, &res); err != nil {
		return nil, err
	}
	out := make([]DownloadVariant, 0, len(res))
	for _, v := range res {
		out = append(out, DownloadVariant{
			Codec:       v.Codec,
			BitrateKbps: v.BitrateInKbps,
			InfoURL:     v.DownloadInfoURL,
			Preview:     v.Preview,
		})
	}
	return out, nil
}

// PickBest выбирает вариант с наибольшим битрейтом, отбрасывая превью.
// Превью — это 30-секундные обрезки, которые приходят без подписки; выбрать
// превью как «лучшее качество» значит молча играть обрезки вместо треков.
func PickBest(vs []DownloadVariant) (DownloadVariant, bool) {
	var best DownloadVariant
	found := false
	for _, v := range vs {
		if v.Preview {
			continue
		}
		if !found || v.BitrateKbps > best.BitrateKbps {
			best, found = v, true
		}
	}
	return best, found
}

// downloadInfoXML — форма XML-документа с данными для сборки прямой ссылки.
type downloadInfoXML struct {
	XMLName xml.Name `xml:"download-info"`
	Host    string   `xml:"host"`
	Path    string   `xml:"path"`
	TS      string   `xml:"ts"`
	S       string   `xml:"s"`
}

// ResolveDirectLink забирает XML по InfoURL и собирает подписанную ссылку.
//
// Результат живёт около минуты, поэтому вызывать этот метод нужно
// непосредственно перед чтением потока, а не заранее при построении очереди.
//
// Загрузка XML — это маленький запрос метаданных, а не поток, поэтому здесь
// используется тот же таймаутированный клиент c.http, которым пользуется do();
// потоковый клиент (HTTPClient) предназначен для самого аудио.
func (c *Client) ResolveDirectLink(ctx context.Context, v DownloadVariant) (string, error) {
	if v.InfoURL == "" {
		return "", errors.New("пустой downloadInfoUrl")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.InfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download-info вернул %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	var info downloadInfoXML
	if err := xml.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("разбор download-info XML: %w", err)
	}
	// SignMP3 безусловно отбрасывает первый символ path — если path пуст,
	// это приведёт к панике среза внутри SignMP3. Проверяем заранее.
	if info.Host == "" || info.Path == "" {
		return "", errors.New("download-info XML не содержит host/path")
	}
	return DirectLinkMP3(info.Host, info.Path, info.TS, info.S), nil
}
