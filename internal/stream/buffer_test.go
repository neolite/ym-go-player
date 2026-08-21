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

	// Путь Retain -> removeLocked проходит и через счётчик size, а не
	// только через карту entries. "b" и "c" — по одному байту каждый.
	const wantSize = int64(2)
	if got := b.Size(); got != wantSize {
		t.Fatalf("Size после Retain = %d, want %d", got, wantSize)
	}
}

// TestPutOverwriteExistingKeyDoesNotDuplicateOrder закрепляет ветку в Put,
// которая при перезаписи существующего ключа сначала удаляет старую запись
// из order (if _, exists := b.entries[id]; exists { b.removeLocked(id) }).
//
// Без этой ветки order копит дубликаты id, и цикл вытеснения в Put
// зацикливается навсегда: removeLocked для уже удалённого id делает
// ранний return, не укорачивая order и не уменьшая size, поэтому условие
// цикла остаётся истинным вечно — под захваченным b.mu, то есть намертво
// блокирует Get, Size и все последующие Put. Запускать с -timeout, чтобы
// зацикливание проявлялось как падение по таймауту, а не висящий прогон.
func TestPutOverwriteExistingKeyDoesNotDuplicateOrder(t *testing.T) {
	b := NewBuffer(10)

	b.Put("a", make([]byte, 2))
	if got, want := b.Size(), int64(2); got != want {
		t.Fatalf("после первого Put(\"a\"): Size = %d, want %d", got, want)
	}

	b.Put("a", make([]byte, 2))
	if got, want := b.Size(), int64(2); got != want {
		t.Fatalf("после перезаписи \"a\": Size = %d, want %d (не удвоенный размер старой записи)", got, want)
	}

	b.Put("b", make([]byte, 9))
	if got, want := b.Size(), int64(9); got != want {
		t.Fatalf("после Put(\"b\"): Size = %d, want %d", got, want)
	}
	if _, ok := b.Get("a"); ok {
		t.Error("\"a\" должен быть вытеснен ради \"b\"")
	}
	got, ok := b.Get("b")
	if !ok || len(got) != 9 {
		t.Fatalf("Get(\"b\") = len %d, ok=%v, want len 9, ok=true", len(got), ok)
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
// задача 12 дёргает Retain при смене очереди. Предел буфера, число
// ключей и размер записей подобраны так, чтобы вытеснение в Put и
// перекраивание order в Retain происходили постоянно под нагрузкой,
// а не оставались недостижимыми ветками. Тест не проверяет конкретные
// значения, зависящие от порядка выполнения — только то, что гонок
// и паник нет (см. go test -race).
func TestBufferIsSafeForConcurrentUse(t *testing.T) {
	b := NewBuffer(64)

	// При пределе 64 байта и записях по 20 байт в буфере помещается едва
	// больше двух ключей одновременно — Put вытесняет постоянно.
	keys := []string{"a", "b", "c", "d", "e"}
	value := make([]byte, 20)

	const goroutines = 32
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := keys[(id+i)%len(keys)]
				switch (id + i) % 4 {
				case 0:
					b.Put(key, value)
				case 1:
					_, _ = b.Get(key)
				case 2:
					// Подмножество меняется от итерации к итерации,
					// так что Retain реально перекраивает order.
					n := 1 + (id+i)%len(keys)
					b.Retain(keys[:n]...)
				case 3:
					_ = b.Size()
				}
			}
		}(g)
	}

	wg.Wait()
}
