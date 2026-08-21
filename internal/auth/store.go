// Package auth хранит OAuth-токен Яндекса вне кода и вне репозитория.
package auth

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNoToken означает, что токен ещё не сохранён.
var ErrNoToken = errors.New("токен не сохранён")

const (
	keyringService = "music212"
	keyringUser    = "yandex-music-oauth"
)

// Store — хранилище единственного токена.
type Store interface {
	Get() (string, error)
	Set(token string) error
	Delete() error
}

type keyringStore struct{}

// NewKeyring возвращает хранилище поверх системного keychain.
func NewKeyring() Store { return keyringStore{} }

func (keyringStore) Get() (string, error) {
	v, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", ErrNoToken
	}
	return v, nil
}

func (keyringStore) Set(token string) error {
	if token == "" {
		return errors.New("пустой токен")
	}
	return keyring.Set(keyringService, keyringUser, token)
}

func (keyringStore) Delete() error {
	err := keyring.Delete(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type memoryStore struct {
	mu    sync.RWMutex
	token string
}

// NewMemory возвращает хранилище в памяти: для тестов и режима без keychain.
func NewMemory() Store { return &memoryStore{} }

func (m *memoryStore) Get() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.token == "" {
		return "", ErrNoToken
	}
	return m.token, nil
}

func (m *memoryStore) Set(token string) error {
	if token == "" {
		return errors.New("пустой токен")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = token
	return nil
}

func (m *memoryStore) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = ""
	return nil
}
