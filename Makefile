.PHONY: run test build vet docker up down logs eval-gen eval

run:
	go run ./cmd/shorttok

test:
	go test ./...

vet:
	gofmt -l . && go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/shorttok ./cmd/shorttok
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/shorttok-eval ./cmd/shorttok-eval

docker:
	docker build -t shorttok:dev .

# Observability stack: ShortTok :8080, Prometheus :9090, Grafana :3000
up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f shorttok

eval-gen:
	go run ./cmd/shorttok-eval gen -n 20 -o testdata/eval/needle.jsonl

# The synthetic conversations are short, so lower the compression threshold:
#   SHORTTOK_HISTORY_MIN_TOKENS=300 make up && make eval
eval:
	go run ./cmd/shorttok-eval run -dataset testdata/eval/needle.jsonl -o eval-report.md
