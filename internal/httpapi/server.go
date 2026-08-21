// Package httpapi отдаёт локальный HTTP-интерфейс демона.
package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Server оборачивает http.Server и слушает только петлевой интерфейс.
type Server struct {
	http *http.Server
	ln   net.Listener
	errc chan error

	// served закрывается, когда горутина Serve из текущей сессии Start
	// фактически завершилась и приняла решение — сообщать об ошибке или
	// нет. Позволяет дождаться реального выхода горутины, а не полагаться
	// на то, что Shutdown успел её дождаться сам.
	served chan struct{}

	// shuttingDown относится к ТЕКУЩЕЙ сессии Start и переприсваивается
	// на новый объект при каждом Start. Горутина Serve захватывает свой
	// объект сессии через замыкание, а не читает поле Server напрямую —
	// это принципиально: Shutdown.Wait() внутри http.Server.Shutdown не
	// синхронизирован с моментом, когда наша горутина дочитает флаг после
	// возврата из Serve, поэтому если бы Start просто сбрасывал общее
	// поле, второй Start мог бы успеть обнулить флаг раньше, чем горутина
	// первой сессии успеет его прочитать, и штатное завершение первой
	// сессии ошибочно утекло бы в errc как "асинхронный отказ". Отдельный
	// объект на сессию исключает эту гонку в принципе.
	shuttingDown *atomic.Bool
}

// New создаёт сервер с заданным обработчиком. Порт выбирается при Start.
//
// Принимает http.Handler, а не конкретно *http.ServeMux: задача 14 должна
// иметь возможность обернуть собранный роутер в OriginGuard (задача 12),
// которая возвращает http.Handler, а не *http.ServeMux. *http.ServeMux сам
// реализует http.Handler, так что вызовы New(mux) без обёртки остаются
// валидными.
func New(handler http.Handler) *Server {
	return &Server{
		http: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		},
		errc: make(chan error, 1),
	}
}

// Start занимает свободный порт на 127.0.0.1 и начинает обслуживание.
func (s *Server) Start() error {
	return s.StartOn("127.0.0.1:0")
}

// StartOn слушает заданный адрес вида host:port. Порт 0 означает
// эфемерный — фактический адрес потом возвращает Addr.
func (s *Server) StartOn(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln

	// Новый флаг на каждую сессию — см. комментарий к полю shuttingDown.
	sessionShuttingDown := &atomic.Bool{}
	s.shuttingDown = sessionShuttingDown

	served := make(chan struct{})
	s.served = served

	go func() {
		defer close(served)

		serveErr := s.http.Serve(ln)
		if serveErr == nil {
			return
		}
		// ErrServerClosed — штатный результат вызова Shutdown для этой же
		// сессии (проверяем флаг именно этой сессии, захваченный
		// замыканием). Любая другая ошибка (включая ErrServerClosed,
		// полученный не от нашего Shutdown, например при переиспользовании
		// http.Server после предыдущего Shutdown) — асинхронный отказ, о
		// котором нельзя молчать.
		if errors.Is(serveErr, http.ErrServerClosed) && sessionShuttingDown.Load() {
			return
		}
		select {
		case s.errc <- serveErr:
		default:
			// В канале уже лежит непрочитанная ошибка — не блокируемся.
		}
	}()
	return nil
}

// Addr возвращает фактический адрес вида 127.0.0.1:54321.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown корректно останавливает сервер.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	if s.shuttingDown != nil {
		s.shuttingDown.Store(true)
	}
	return s.http.Shutdown(ctx)
}

// Err возвращает канал, в который попадает ошибка, если Serve завершился
// не из-за штатного вызова Shutdown. Канал буферизован на одно значение;
// вызывающая сторона слушает его наравне с сигналами ОС, чтобы не молчать
// при асинхронном отказе сервера.
func (s *Server) Err() <-chan error {
	return s.errc
}
