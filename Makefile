.PHONY: test build run
test:
	go test ./... -count=1
build:
	go build -o musicd ./cmd/musicd
run: build
	./musicd
