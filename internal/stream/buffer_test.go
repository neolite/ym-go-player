package stream

import (
	"sync"
	"testing"
)

func TestBufferRoundTrip(t *testing.T) {
	b := NewBuffer(1024)
	if _, ok := b.Get("a"); ok {
		t.Fatal("пустой буфер не должен ничего отдавать")
	}
	b.Put("a", []byte("hello"))
	got, ok := b.Get("a")
	if !ok || string(got) != "hello" {
		t.Fatalf("Get = %q, ok=%v", got, ok)
	}
}

func TestBufferEvictsOldestOverCap(t *testing.T) {
	b := NewBuffer(10)
	b.Put("a", make([]byte, 6))
	b.Put("b", make([]byte, 6))

	if _, ok := b.Get("a"); ok {
		t.Fatal("самая старая запись должна быть вытеснена")
	}
	if _, ok := b.Get("b"); !ok {
		t.Fatal("свежая запись должна остаться")
	}
	if b.Size() > 10 {
		t.Fatalf("Size = %d, превышает предел", b.Size())
	}
}

func TestBufferRejectsEntryLargerThanCap(t *testing.T) {
	b := NewBuffer(10)
	b.Put("big", make([]byte, 20))
	if _, ok := b.Get("big"); ok {
		t.Fatal("запись больше всего буфера не должна сохраняться")
	}
}

// Retain — то, как буфер следует за очередью: всё, кроме текущего
// и следующего трека, выбрасывается.
func TestRetainDropsEverythingElse(t *testing.T) {
	b := NewBuffer(1024)
	b.Put("a", []byte("1"))
	b.Put("b", []byte("2"))
	b.Put("c", []byte("3"))

	b.Retain("b", "c")

	if _, ok := b.Get("a"); ok {
		t.Fatal("a должен быть выброшен")
	}
	if _, ok := b.Get("b"); !ok {
		t.Fatal("b должен остаться")
	}
	if _, ok := b.Get("c"); !ok {
		t.Fatal("c должен остаться")
	}
}

func TestClearEmptiesBuffer(t *testing.T) {
	b := NewBuffer(1024)
	b.Put("a", []byte("1"))
	b.Clear()
	if b.Size() != 0 {
		t.Fatalf("Size после Clear = %d, want 0", b.Size())
	}
}

// TestNewBufferFallsBackToDefaultCap проверяет, что NewBuffer с
// неположительным пределом не превращает буфер в бесполезный —
// запись заведомо больше 10-байтовых лимитов остальных тестов
// должна и сохраниться, и вернуться через Get.
func TestNewBufferFallsBackToDefaultCap(t *testing.T) {
	const big = 2 << 20 // 2 МБ

	for _, maxBytes := range []int64{0, -1} {
		b := NewBuffer(maxBytes)
		data := make([]byte, big)
		b.Put("big", data)
		got, ok := b.Get("big")
		if !ok || len(got) != big {
			t.Fatalf("maxBytes=%d: Get = len %d, ok=%v, want len %d, ok=true", maxBytes, len(got), ok, big)
		}
	}
}

// TestBufferIsSafeForConcurrentUse нагружает буфер конкурентно: задача 8
// читает и пишет байты треков из параллельных HTTP-обработчиков, пока
// задача 12 дёргает Retain при смене очереди. Тест не проверяет
// конкретные значения, зависящие от порядка выполнения — только то,
// что гонок и паник нет (см. go test -race).
func TestBufferIsSafeForConcurrentUse(t *testing.T) {
	b := NewBuffer(1024)

	const goroutines = 32
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (id + i) % 4 {
				case 0:
					b.Put("a", []byte("data"))
				case 1:
					_, _ = b.Get("a")
				case 2:
					b.Retain("a")
				case 3:
					_ = b.Size()
				}
			}
		}(g)
	}

	wg.Wait()
}
