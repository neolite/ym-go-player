// Package stream проксирует аудиопоток и держит транзитный буфер.
package stream

import "sync"

// DefaultMaxBytes — страховочный предел буфера (256 МБ).
// Основное ограничение задаётся вызовом Retain: текущий и следующий трек.
const DefaultMaxBytes int64 = 256 << 20

// Buffer — транзитное хранилище байтов треков в памяти процесса.
// Не пишет на диск и не переживает завершение программы: это буфер
// воспроизведения, а не библиотека.
type Buffer struct {
	mu       sync.Mutex
	entries  map[string][]byte
	order    []string
	maxBytes int64
	size     int64
}

// NewBuffer создаёт буфер с заданным потолком в байтах.
func NewBuffer(maxBytes int64) *Buffer {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Buffer{entries: make(map[string][]byte), maxBytes: maxBytes}
}

// Get возвращает байты трека, если они есть в буфере.
//
// Слайс возвращается без копии — вызывающий обязан только читать его,
// не мутировать. Сам буфер уже сохранённые байты никогда не меняет,
// поэтому такой контракт безопасен: например, http.ServeContent поверх
// bytes.NewReader содержимое не трогает.
func (b *Buffer) Get(id string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.entries[id]
	return data, ok
}

// Put кладёт байты трека, вытесняя самые старые записи при переполнении.
// Запись, которая одна не помещается в буфер, отбрасывается целиком.
func (b *Buffer) Put(id string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := int64(len(data))
	if n > b.maxBytes {
		return
	}
	if _, exists := b.entries[id]; exists {
		b.removeLocked(id)
	}
	for b.size+n > b.maxBytes && len(b.order) > 0 {
		b.removeLocked(b.order[0])
	}
	b.entries[id] = data
	b.order = append(b.order, id)
	b.size += n
}

// Retain оставляет только перечисленные записи. Так буфер следует за очередью.
func (b *Buffer) Retain(ids ...string) {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range append([]string(nil), b.order...) {
		if !keep[id] {
			b.removeLocked(id)
		}
	}
}

// Clear опустошает буфер. Вызывается при завершении работы.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = make(map[string][]byte)
	b.order = nil
	b.size = 0
}

// Size — текущий занятый объём в байтах.
func (b *Buffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// removeLocked удаляет запись. Вызывать только под захваченным мьютексом.
func (b *Buffer) removeLocked(id string) {
	data, ok := b.entries[id]
	if !ok {
		return
	}
	b.size -= int64(len(data))
	delete(b.entries, id)
	for i, v := range b.order {
		if v == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}
