// Command musicd — локальный демон плеера Яндекс Музыки.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"music212/internal/httpapi"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := httpapi.New(mux)
	if err := srv.Start(); err != nil {
		log.Fatalf("не удалось запустить сервер: %v", err)
	}
	fmt.Printf("плеер слушает http://%s\n", srv.Addr())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("ошибка остановки: %v", err)
	}
}
