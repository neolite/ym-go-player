// Command musicd — локальный демон плеера Яндекс Музыки.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"music212/internal/auth"
	"music212/internal/httpapi"
	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

func main() {
	noKeychain := flag.Bool("no-keychain", false, "хранить токен только в памяти процесса")
	noOpen := flag.Bool("no-open", false, "не открывать браузер при старте")
	flag.Parse()

	var store auth.Store = auth.NewKeyring()
	if *noKeychain {
		store = auth.NewMemory()
	}

	authHandler := httpapi.NewAuth(store, httpapi.DefaultVerify)
	buffer := stream.NewBuffer(stream.DefaultMaxBytes)

	newClient := func() (*ymapi.Client, error) {
		token, err := authHandler.Token()
		if err != nil {
			return nil, err
		}
		return ymapi.New(token), nil
	}

	app := &httpapi.App{
		Auth:   authHandler,
		Queue:  player.NewQueue(),
		Hub:    httpapi.NewHub(),
		Buffer: buffer,
		Client: newClient,
	}

	// Отдельный клиент для потоковой загрузки треков — по образцу
	// internal/ymapi.Client.stream (см. internal/ymapi/client.go). Timeout
	// здесь намеренно равен нулю: http.Client.Timeout покрывает весь цикл
	// запроса целиком, включая чтение тела ответа, а трек на медленном
	// соединении может качаться дольше пяти минут — общий таймаут оборвал
	// бы его посередине. Ограничена только фаза получения заголовков
	// (ResponseHeaderTimeout): зависший источник не держит нас вечно.
	// Верхнюю границу самой загрузки задаёт leaderTimeout прокси (задача
	// 8, internal/stream/proxy.go), а не таймаут этого клиента.
	streamClient := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 20 * time.Second,
		},
	}
	app.Proxy = stream.NewProxy(resolverFunc(newClient), buffer, streamClient)

	mux := app.Routes()
	mux.Handle("/", httpapi.StaticHandler())
	// /healthz — дешёвая проверка того, что демон жив, не трогающая ни API,
	// ни авторизацию. Регистрируется здесь же, до OriginGuard: это более
	// специфичный шаблон, чем "/", поэтому порядок регистрации значения не
	// имеет — http.ServeMux в любом случае отдаст предпочтение ему.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Статика и /healthz домонтированы выше; теперь оборачиваем весь
	// роутер целиком защитой по Origin — иначе любая сторонняя вкладка в
	// том же браузере сможет слать команды на локальный порт демона.
	srv := httpapi.New(httpapi.OriginGuard(mux))
	if err := srv.Start(); err != nil {
		log.Fatalf("не удалось запустить сервер: %v", err)
	}
	url := "http://" + srv.Addr()
	fmt.Printf("плеер слушает %s\n", url)
	if !*noOpen {
		if err := openBrowser(url); err != nil {
			log.Printf("не удалось открыть браузер: %v; откройте адрес вручную", err)
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Ждём либо сигнал завершения от ОС, либо неожиданную гибель сервера —
	// в последнем случае не молчим, а завершаемся с понятным сообщением.
	// Без этого select программа могла бы напечатать "плеер слушает" и
	// адрес уже после того, как Serve умер, оставив пользователя перед
	// экраном отказа в соединении без единой подсказки в терминале.
	select {
	case <-stop:
		// штатное завершение
	case err := <-srv.Err():
		log.Fatalf("сервер неожиданно остановился: %v", err)
	}

	fmt.Println("\nостанавливаюсь…")
	buffer.Clear() // буфер не переживает завершение — это требование скоупа
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("ошибка остановки: %v", err)
	}
}

// resolverFunc адаптирует фабрику клиентов к интерфейсу stream.Resolver.
type resolverFunc func() (*ymapi.Client, error)

func (f resolverFunc) ResolveTrack(ctx context.Context, trackID string) (string, error) {
	c, err := f()
	if err != nil {
		return "", err
	}
	return c.ResolveTrack(ctx, trackID)
}

// openBrowser открывает адрес демона в браузере по умолчанию. Неудача не
// фатальна: адрес уже напечатан в терминале и его можно открыть вручную —
// вызывающая сторона логирует ошибку и на этом успокаивается.
func openBrowser(url string) error {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "explorer"
	default:
		cmd = "xdg-open"
	}
	return exec.Command(cmd, url).Start()
}
