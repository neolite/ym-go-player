package auth

import (
	"errors"
	"testing"
)

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewMemory()

	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Get на пустом хранилище = %v, want ErrNoToken", err)
	}
	if err := s.Set("secret-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "secret-token" {
		t.Fatalf("Get = %q, want %q", got, "secret-token")
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Get после Delete = %v, want ErrNoToken", err)
	}
}

func TestMemoryStoreRejectsEmptyToken(t *testing.T) {
	s := NewMemory()
	if err := s.Set(""); err == nil {
		t.Fatal("Set(\"\") должен возвращать ошибку")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := NewMemory()
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete на пустом хранилище должен быть безошибочным, got %v", err)
	}
}
