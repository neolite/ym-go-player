package httpapi

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServerServesHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := New(mux)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerBindsLoopbackOnly(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if got := srv.Addr(); len(got) < 10 || got[:10] != "127.0.0.1:" {
		t.Fatalf("Addr = %q, want 127.0.0.1:<port>", got)
	}
}

// TestStartOnFixedAddress проверяет, что StartOn слушает ровно запрошенный
// адрес — на этом держится флаг -addr демона и постоянный порт интерфейса.
func TestStartOnFixedAddress(t *testing.T) {
	// Берём свободный порт у ОС и сразу освобождаем: окно гонки между
	// Close и StartOn микроскопично, а гарантированно свободный порт иначе
	// не получить.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe Listen: %v", err)
	}
	addr := probe.Addr().String()
	probe.Close()

	srv := New(http.NewServeMux())
	if err := srv.StartOn(addr); err != nil {
		t.Fatalf("StartOn(%q): %v", addr, err)
	}
	defer srv.Shutdown(context.Background())

	if got := srv.Addr(); got != addr {
		t.Fatalf("Addr = %q, want %q", got, addr)
	}
}

// TestServerReportsAsyncServeFailure проверяет, что сервер не молчит,
// если Serve завершился не из-за штатного вызова Shutdown — например,
// слушатель закрыли в обход Server.Shutdown.
func TestServerReportsAsyncServeFailure(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Симулируем асинхронный отказ Serve: закрываем слушатель напрямую,
	// минуя Shutdown, — так же, как повело бы себя ОС при неожиданной
	// потере сокета.
	if err := srv.ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}

	select {
	case err := <-srv.Err():
		if err == nil {
			t.Fatal("Err() вернул nil, ожидалась ошибка")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Err() не сообщил об асинхронном отказе Serve за отведённое время")
	}
}

// TestServerShutdownIsNotReportedAsError проверяет, что штатная остановка
// через Shutdown не попадает в Err() как ошибка. Дожидаемся закрытия
// srv.served, чтобы убедиться, что горутина Serve реально завершилась и
// приняла решение не слать ошибку, — иначе тест мог бы пройти просто
// потому, что горутина ещё не успела отработать.
func TestServerShutdownIsNotReportedAsError(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case <-srv.served:
		// горутина Serve завершилась — можно проверять итог.
	case <-time.After(2 * time.Second):
		t.Fatal("горутина Serve не завершилась после Shutdown за отведённое время")
	}

	select {
	case err := <-srv.Err():
		t.Fatalf("Err() сообщил об ошибке при штатном Shutdown: %v", err)
	default:
		// ожидаемо: канал пуст — штатное завершение ошибкой не считается.
	}
}

// TestServerStartAfterShutdownReportsError проверяет именно тот сценарий
// отказа, из-за которого возникла эта находка: http.Server нельзя
// переиспользовать после Shutdown. Если Start вызван на уже остановленном
// сервере, Serve сразу возвращает ErrServerClosed сам по себе — это не
// результат текущего Shutdown, и такую ошибку нельзя молча проглатывать.
func TestServerStartAfterShutdownReportsError(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start #1: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Барьер: дожидаемся, что горутина сессии 1 действительно завершилась
	// и приняла решение не слать ошибку, — иначе гонка между Shutdown и
	// Start #2 (см. ниже) может подсунуть в Err() штатное завершение
	// сессии 1 вместо ошибки реального переиспользования сервера.
	<-srv.served
	select {
	case err := <-srv.Err():
		t.Fatalf("штатный Shutdown сессии 1 попал в Err(): %v", err)
	default:
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start #2: %v", err)
	}

	select {
	case err := <-srv.Err():
		if err == nil {
			t.Fatal("Err() вернул nil, ожидалась ошибка переиспользования сервера")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Err() не сообщил об ошибке повторного Start после Shutdown")
	}
}
