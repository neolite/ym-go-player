// Package httpapi отдаёт локальный HTTP-интерфейс демона.
package httpapi

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Server оборачивает http.Server и слушает только петлевой интерфейс.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// New создаёт сервер с заданным роутером. Порт выбирается при Start.
func New(mux *http.ServeMux) *Server {
	return &Server{
		http: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start занимает свободный порт на 127.0.0.1 и начинает обслуживание.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	go s.http.Serve(ln)
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
	return s.http.Shutdown(ctx)
}
