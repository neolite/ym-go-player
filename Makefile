.PHONY: test build run web app
web:
	mkdir -p internal/httpapi/dist
	cd web && npx --yes esbuild src/app.ts --bundle --minify --outfile=../internal/httpapi/dist/app.js
	cp web/index.html internal/httpapi/dist/index.html
test:
	go test ./... -count=1
build: web
	go build -o musicd ./cmd/musicd
run: build
	./musicd -addr 127.0.0.1:61588
# Нативный клиент (Wails, webview без браузера). Теги desktop,production
# обычно подставляет wails CLI — здесь обходимся голым go build.
# -framework UniformTypeIdentifiers нужен при сборке с SDK из Xcode-beta:
# UTType оттуда переехал в этот фреймворк, и линковка без него падает.
app: web
	CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags "desktop,production" -o musicapp ./cmd/musicapp
	./musicapp
