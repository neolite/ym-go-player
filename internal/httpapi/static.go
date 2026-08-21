package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// //go:embed требование времени компиляции, а не выполнения: каталог dist
// обязан существовать и быть непустым, иначе не соберётся весь пакет и
// вместе с ним весь `go build ./...`/`go test ./...` репозитория. Поэтому в
// репозитории всегда закоммичен internal/httpapi/dist/.gitkeep — префикс
// all: специально включает файлы, начинающиеся с точки, ради него. Сам
// собранный фронтенд (app.js, index.html) в git не попадает — его кладёт
// туда цель `make web`.
//
//go:embed all:dist
var distFS embed.FS

// StaticHandler отдаёт собранный фронтенд из вшитой файловой системы.
// Фронтенд собирается отдельно целью `make web`; без неё в dist лежит
// только .gitkeep. В этом случае обработчик не вернёт 404 — http.FileServer
// без index.html отдаёт листинг каталога (со списком .gitkeep). Это не
// поломка, а стандартное поведение http.FileServer; следующему читателю
// незачем чинить то, что не сломано.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
