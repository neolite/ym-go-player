package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// YandexClientID и YandexClientSecret — официальные ключи Yandex Music Android client.
	YandexClientID     = "23cabbbdc6cd418abb4b39c32c41195d"
	YandexClientSecret = "53bc75238f0c4d08a118e51fe9203300"

	DefaultDeviceCodeURL = "https://oauth.yandex.ru/device/code"
	DefaultTokenURL      = "https://oauth.yandex.ru/token"
)

// ErrPending возвращается, когда пользователь ещё не подтвердил код на странице Яндекса.
var ErrPending = errors.New("authorization_pending")

// DeviceCodeResponse содержит результат первого шага Device Flow.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceTokenResponse содержит токен или ошибку поллинга.
type DeviceTokenResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// DeviceClient выносит выполнение запросов OAuth Device Flow.
type DeviceClient struct {
	HTTPClient    *http.Client
	ClientID      string
	ClientSecret  string
	DeviceCodeURL string
	TokenURL      string
}

// NewDeviceClient возвращает стандартный клиент Device Flow.
func NewDeviceClient() *DeviceClient {
	return &DeviceClient{
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		ClientID:      YandexClientID,
		ClientSecret:  YandexClientSecret,
		DeviceCodeURL: DefaultDeviceCodeURL,
		TokenURL:      DefaultTokenURL,
	}
}

// RequestCode запрашивает у Яндекса код устройства.
func (c *DeviceClient) RequestCode(ctx context.Context, deviceID string) (*DeviceCodeResponse, error) {
	if deviceID == "" {
		deviceID = randomHex(5)
	}
	data := url.Values{}
	data.Set("client_id", c.ClientID)
	data.Set("device_id", deviceID)
	data.Set("device_name", "music-212")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.DeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("запрос кода устройства завершился с ошибкой %d: %s", resp.StatusCode, string(body))
	}

	var res DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа кода устройства: %w", err)
	}
	return &res, nil
}

// PollToken проверяет статус подтверждения кода и запрашивает токен.
func (c *DeviceClient) PollToken(ctx context.Context, deviceCode string) (*DeviceTokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "device_code")
	data.Set("code", deviceCode)
	data.Set("client_id", c.ClientID)
	data.Set("client_secret", c.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res DeviceTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа токена устройства: %w", err)
	}

	if res.Error == "authorization_pending" {
		return nil, ErrPending
	}
	if res.Error != "" {
		if res.ErrorDescription != "" {
			return nil, errors.New(res.ErrorDescription)
		}
		return nil, fmt.Errorf("ошибка авторизации: %s", res.Error)
	}
	if res.AccessToken == "" {
		return nil, errors.New("пустой access_token в ответе")
	}

	return &res, nil
}

func randomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
