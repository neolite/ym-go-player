// Command musicapp — нативный клиент плеера Яндекс Музыки: то же
// состояние и тот же фронтенд, что у демона, но в собственном окне
// (webview), без браузера.
//
// Архитектурная граница «демон отдаёт HTTP» позволяет не менять логику
// вовсе: внутри процесса поднимается тот самый сервер из internal/app на
// случайном порту loopback, а окно webview на него перенаправляется.
// Обслуживание запросов окна через AssetServer (in-process, без порта)
// проверено и отклонено: часть запросов фронта (SSE /api/events) через
// него не доходит, окно остаётся пустым. TCP на 127.0.0.1 — ровно тот
// путь, который годами проверен в браузере, и webview его понимает.
// Токен общий с демоном: оба читают его из системной связки ключей.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"music212/internal/app"
	"music212/internal/auth"
	"music212/internal/httpapi"
)

func main() {
	handler, buffer := app.New(auth.NewKeyring())
	srv := httpapi.New(handler)
	if err := srv.StartOn("127.0.0.1:0"); err != nil {
		log.Fatalf("не удалось запустить встроенный сервер: %v", err)
	}
	url := "http://" + srv.Addr()
	log.Printf("встроенный сервер слушает %s", url)

	// Окно стартует на схеме wails:// — отдаём оттуда единственную
	// страницу-плашку, которая немедленно переносит webview на адрес
	// встроенного сервера. <meta refresh> — навигация документа, в отличие
	// от скрипта она не зависит от политик webview.
	bootstrap := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=%s">`, url)
	bootstrapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, bootstrap)
	})

	err := wails.Run(&options.App{
		Title:     "Музыка",
		Width:     1180,
		Height:    840,
		MinWidth:  780,
		MinHeight: 620,
		// Цвет фона окна до прорисовки веб-слоя — тёмный, как тёмная базовая
		// тема: без белой вспышки при запуске у пользователей тёмной системы.
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 19, A: 255},
		AssetServer: &assetserver.Options{
			Handler: bootstrapHandler,
		},
		Mac: &mac.Options{
			// Родной заголовок macOS — окно должно выглядеть нативно,
			// а не как встроенный браузер.
			TitleBar: mac.TitleBarDefault(),
		},
		OnShutdown: func(_ context.Context) {
			buffer.Clear() // буфер не переживает завершение — требование скоупа
			// SSE и стрим-соединения висят долго: Shutdown их ждёт, поэтому
			// даём 5 с, а если не хватило — не ругаемся: процесс всё равно
			// завершается, и разорванные соединения не страшны.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				log.Printf("ошибка остановки встроенного сервера: %v", err)
			}
		},
	})
	if err != nil {
		log.Fatalf("не удалось запустить окно: %v", err)
	}
}
