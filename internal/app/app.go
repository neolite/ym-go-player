// Package app собирает полный HTTP-обработчик плеера: API, статика,
// потоковый прокси, healthz и защита по Origin. Сборка вынесена из
// cmd/musicd, чтобы её же, не дублируя, использовал нативный клиент
// (cmd/musicapp) — там обработчик отдаётся webview напрямую, без порта.
package app

import (
	"context"
	"fmt"
	"net/http"

	"music212/internal/auth"
	"music212/internal/httpapi"
	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

// New собирает обработчик целиком и возвращает его вместе с буфером —
// вызывающий обязан очистить буфер при завершении (он не переживает
// выход процесса, это требование скоупа).
func New(store auth.Store) (http.Handler, *stream.Buffer) {
	authHandler := httpapi.NewAuth(store, httpapi.DefaultVerify)
	buffer := stream.NewBuffer(stream.DefaultMaxBytes)

	newClient := func() (*ymapi.Client, error) {
		token, err := authHandler.Token()
		if err != nil {
			return nil, err
		}
		return ymapi.New(token), nil
	}

	a := &httpapi.App{
		Auth:   authHandler,
		Queue:  player.NewQueue(),
		Hub:    httpapi.NewHub(),
		Buffer: buffer,
		Client: newClient,
	}

	// Потоковый клиент не собираем здесь заново — берём его у ymapi-клиента
	// через HTTPClient(): иначе клонирование http.DefaultTransport с
	// ResponseHeaderTimeout дублировалось бы в двух пакетах и рано или поздно
	// разошлось бы по значениям. Токен на этот клиент не влияет (подписанные
	// ссылки качаются без заголовка Authorization — см. Proxy.download),
	// поэтому пустая строка в конструкторе безопасна; токен нужен только
	// запросам метаданных через Get/PostForm/PostJSON.
	streamClient := ymapi.New("").HTTPClient()
	a.Proxy = stream.NewProxy(resolverFunc(newClient), buffer, streamClient)

	mux := a.Routes()
	mux.Handle("/", httpapi.StaticHandler())
	// /healthz — дешёвая проверка того, что процесс жив, не трогающая ни
	// API, ни авторизацию. Это более специфичный шаблон, чем "/", поэтому
	// порядок регистрации значения не имеет — http.ServeMux в любом случае
	// отдаст предпочтение ему.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Статика и /healthz домонтированы выше; теперь оборачиваем весь
	// роутер целиком защитой по Origin — иначе любая сторонняя вкладка в
	// том же браузере сможет слать команды на локальный порт демона.
	// Webview нативного клиента заголовок Origin не ставит — его такая
	// обёртка не задевает.
	return httpapi.OriginGuard(mux), buffer
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
