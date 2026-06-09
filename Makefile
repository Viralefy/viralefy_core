.PHONY: dev run build test lint fmt migrate docker clean

dev: run

run:
	go run ./cmd/api

build:
	go build -o bin/viralefy-api ./cmd/api

test:
	go test ./...

lint:
	go vet ./...

fmt:
	go fmt ./...

migrate:
	@echo "Migrations run automatically on startup"

docker:
	docker build -t viralefy-api .

clean:
	rm -rf bin/
