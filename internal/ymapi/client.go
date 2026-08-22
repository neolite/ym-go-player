package ymapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL — точка входа неофициального API.
const DefaultBaseURL = "https://api.music.yandex.net"

// userAgent имитирует официальный клиент: часть эндпоинтов на это смотрит.
const userAgent = "Yandex-Music-API"

var (
	// ErrUnauthorized — токен невалиден или истёк.
	ErrUnauthorized = errors.New("токен невалиден или истёк")
	// ErrForbidden — доступ запрещён: чаще всего нет подписки или регион.
	ErrForbidden = errors.New("доступ запрещён")
)

// Client — транспорт к API Яндекса. Токен не покидает эту структуру.
type Client struct {
	token string
	base  string
	// http — клиент для запросов метаданных (JSON-эндпоинты). Имеет общий
	// таймаут на весь цикл запроса — эти ответы всегда короткие.
	http *http.Client
	// stream — клиент для загрузки потоков (задача 6). У него намеренно нет
	// общего Timeout: http.Client.Timeout покрывает весь цикл запроса, включая
	// чтение тела ответа, а трек может качаться дольше 20 секунд на слабой
	// сети. Вместо этого ограничена только фаза получения заголовков ответа
	// (ResponseHeaderTimeout) — зависший сервер не держит нас вечно. Отмену
	// долгой загрузки должен обеспечивать контекст конкретного запроса.
	stream *http.Client
}

// New создаёт клиент к боевому API.
func New(token string) *Client { return NewWithBase(token, DefaultBaseURL) }

// NewWithBase позволяет подменить адрес — используется в тестах.
func NewWithBase(token, base string) *Client {
	// Транспорт для stream клонируется из http.DefaultTransport, а не
	// собирается голым литералом &http.Transport{...}: у голого литерала
	// нет Proxy (значит, за обязательным корпоративным HTTP(S)_PROXY
	// скачивание трека молча не соединяется, тогда как c.http его
	// уважает — расхождение почти невозможно связать с настройкой прокси
	// по симптому), нет ForceAttemptHTTP2 и нет пула соединений
	// DefaultTransport. Clone() возвращает независимую копию — правка
	// её полей ниже не затрагивает сам http.DefaultTransport.
	streamTransport := http.DefaultTransport.(*http.Transport).Clone()
	streamTransport.ResponseHeaderTimeout = 20 * time.Second
	return &Client{
		token: token,
		base:  strings.TrimRight(base, "/"),
		http:  &http.Client{Timeout: 20 * time.Second},
		stream: &http.Client{
			Transport: streamTransport,
		},
	}
}

// HTTPClient отдаёт клиент для потоковой загрузки треков — без общего
// дедлайна на весь запрос (см. комментарий к полю stream). Единственное
// назначение этого метода — отдавать именно потоковый клиент; для запросов
// метаданных пакет использует Get/PostForm/PostJSON, которые берут свой,
// таймаутированный клиент самостоятельно.
func (c *Client) HTTPClient() *http.Client { return c.stream }

// Get выполняет GET и разбирает конверт {"result": ...} в out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// PostForm выполняет POST формы и разбирает конверт в out.
func (c *Client) PostForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

// PostJSON выполняет POST с телом JSON.
func (c *Client) PostJSON(ctx context.Context, path string, query url.Values, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "OAuth "+c.token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("api вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("разбор конверта: %w", err)
	}
	trimmed := bytes.TrimSpace(envelope.Result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		// json.Unmarshal([]byte("null"), out) — задокументированный no-op:
		// он не трогает out и не возвращает ошибку. Если не отсекать этот
		// случай явно, {"result":null} проходит как валидный пустой ответ,
		// и вызывающий код получает нулевую структуру вместо диагностики.
		return errors.New("result в ответе отсутствует или null")
	}
	return json.Unmarshal(envelope.Result, out)
}

// nowUnixFloat вынесен отдельно, чтобы тесты могли подменять время
// без обращения к системным часам.
var nowUnixFloat = func() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// nowISO8601 — то же для канала /play-audio: он требует ISO 8601
// с миллисекундами (YYYY-MM-DDThh:mm:ss.SSSZ), а не unix-метку.
var nowISO8601 = func() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
