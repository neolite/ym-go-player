.PHONY: test build run web
web:
	mkdir -p internal/httpapi/dist
	cd web && npx --yes esbuild src/app.ts --bundle --minify --outfile=../internal/httpapi/dist/app.js
	cp web/index.html internal/httpapi/dist/index.html
test:
	go test ./... -count=1
build: web
	go build -o musicd ./cmd/musicd
run: build
	./musicd
