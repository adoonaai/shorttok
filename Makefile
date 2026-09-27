.PHONY: run test build vet docker

run:
	go run ./cmd/shorttok

test:
	go test ./...

vet:
	gofmt -l . && go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/shorttok ./cmd/shorttok

docker:
	docker build -t shorttok:dev .
