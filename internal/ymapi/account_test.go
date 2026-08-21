package ymapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const accountStatusFixture = `{"result":{
  "account":{"uid":1234567,"login":"tester","region":225},
  "plus":{"hasPlus":true}
}}`

func TestAccountStatusParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/status" {
			t.Errorf("path = %q, want /account/status", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "OAuth test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(accountStatusFixture))
	}))
	defer srv.Close()

	c := NewWithBase("test-token", srv.URL)
	got, err := c.AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	if got.UID != 1234567 || got.Login != "tester" || got.Region != 225 || !got.HasPlus {
		t.Fatalf("AccountStatus = %+v", got)
	}
}

func TestUnauthorizedIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewWithBase("bad", srv.URL)
	_, err := c.AccountStatus(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestForbiddenIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewWithBase("t", srv.URL)
	_, err := c.AccountStatus(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}
