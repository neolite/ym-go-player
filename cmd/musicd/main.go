// Command musicd — локальный демон плеера Яндекс Музыки.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"music212/internal/app"
	"music212/internal/auth"
	"music212/internal/httpapi"
)

func main() {
	noKeychain := flag.Bool("no-keychain", false, "хранить токен только в памяти процесса")
	noOpen := flag.Bool("no-open", false, "не открывать браузер при старте")
	addr := flag.String("addr", "127.0.0.1:0", "адрес интерфейса (по умолчанию случайный порт)")
	flag.Parse()

	var store auth.Store = auth.NewKeyring()
	if *noKeychain {
		store = auth.NewMemory()
	}

	handler, buffer := app.New(store)
	srv := httpapi.New(handler)
	if err := srv.StartOn(*addr); err != nil {
		log.Fatalf("не удалось запустить сервер на %s: %v", *addr, err)
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

// openBrowser открывает адрес демона в браузере по умолчанию. Неудача не
// фатальна: адрес уже напечатан в терминале и его можно открыть вручную —
// вызывающая сторона логирует ошибку и на этом успокаивается.
//
// Wait() дожидается завершения потомка в отдельной горутине, а не в этой
// функции: сам процесс open/xdg-open завершается почти сразу (он лишь
// передаёт адрес уже запущенному браузеру), но без Wait() он остаётся
// зомби на всё время жизни демона. Ошибку Wait() не логируем — открытый
// браузер мог закрыться как угодно, это не повод для сообщения в лог.
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
	c := exec.Command(cmd, url)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
