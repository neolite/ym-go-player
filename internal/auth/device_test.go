package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceClient_RequestCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm failed: %v", err)
		}
		if r.FormValue("client_id") != YandexClientID {
			t.Errorf("expected client_id %s, got %s", YandexClientID, r.FormValue("client_id"))
		}
		if r.FormValue("device_name") != "music-212" {
			t.Errorf("expected device_name music-212, got %s", r.FormValue("device_name"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "dev_123",
			UserCode:        "ABCD-1234",
			VerificationURL: "https://oauth.yandex.ru/activate",
			ExpiresIn:       300,
			Interval:        5,
		})
	}))
	defer ts.Close()

	cli := &DeviceClient{
		HTTPClient:    ts.Client(),
		ClientID:      YandexClientID,
		ClientSecret:  YandexClientSecret,
		DeviceCodeURL: ts.URL,
		TokenURL:      ts.URL,
	}

	res, err := cli.RequestCode(context.Background(), "my_device")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeviceCode != "dev_123" || res.UserCode != "ABCD-1234" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestDeviceClient_PollToken_PendingAndSuccess(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm failed: %v", err)
		}
		if r.FormValue("grant_type") != "device_code" {
			t.Errorf("expected grant_type device_code, got %s", r.FormValue("grant_type"))
		}
		if r.FormValue("code") != "dev_123" {
			t.Errorf("expected code dev_123, got %s", r.FormValue("code"))
		}

		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(DeviceTokenResponse{
				Error:            "authorization_pending",
				ErrorDescription: "user has not entered code yet",
			})
			return
		}

		_ = json.NewEncoder(w).Encode(DeviceTokenResponse{
			AccessToken: "test_oauth_token_123",
			TokenType:   "bearer",
			ExpiresIn:   31536000,
		})
	}))
	defer ts.Close()

	cli := &DeviceClient{
		HTTPClient:    ts.Client(),
		ClientID:      YandexClientID,
		ClientSecret:  YandexClientSecret,
		DeviceCodeURL: ts.URL,
		TokenURL:      ts.URL,
	}

	// First call: pending
	_, err := cli.PollToken(context.Background(), "dev_123")
	if !errors.Is(err, ErrPending) {
		t.Fatalf("expected ErrPending, got %v", err)
	}

	// Second call: success
	res, err := cli.PollToken(context.Background(), "dev_123")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if res.AccessToken != "test_oauth_token_123" {
		t.Errorf("expected access token test_oauth_token_123, got %s", res.AccessToken)
	}
}
