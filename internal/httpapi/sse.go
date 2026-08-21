package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"music212/internal/player"
)

// subscriberBuffer — сколько кадров состояния держим для подписчика,
// прежде чем считать его медленным.
const subscriberBuffer = 8

// Hub рассылает состояние плеера всем открытым вкладкам.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan player.State]struct{}
}

// NewHub создаёт пустой хаб.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan player.State]struct{})}
}

// Subscribe регистрирует подписчика и возвращает функцию отписки.
func (h *Hub) Subscribe() (<-chan player.State, func()) {
	ch := make(chan player.State, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Broadcast рассылает состояние. Медленный подписчик пропускает кадр,
// но никогда не блокирует остальных: состояние самодостаточно, и потеря
// промежуточного кадра ничего не ломает.
func (h *Hub) Broadcast(st player.State) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

// HandleSSE держит открытое соединение и стримит состояние.
func (h *Hub) HandleSSE(w http.ResponseWriter, r *http.Request, initial player.State) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "стриминг не поддерживается", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := h.Subscribe()
	defer cancel()

	writeFrame(w, initial)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case st, open := <-ch:
			if !open {
				return
			}
			writeFrame(w, st)
			flusher.Flush()
		}
	}
}

func writeFrame(w http.ResponseWriter, st player.State) {
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
}
