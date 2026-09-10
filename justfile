set shell := ["bash", "-euo", "pipefail", "-c"]
set windows-shell := ["powershell.exe", "-NoLogo", "-NoProfile", "-Command"]

default:
    @just --list

# Complete local CI; no Node/Jest orchestration.
check: go-check lint docs-check

go-check:
    go test -race -shuffle=on -count=1 "-coverprofile=coverage.out" ./...
    go run ./cmd/devcheck coverage coverage.out 85

lint:
    go run ./cmd/devcheck format
    go vet ./...
    go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

docs-check:
    go run ./cmd/devcheck docs
    go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes= -config-file .github/actionlint.yaml .github/workflows/ci.yml .github/workflows/runner.yml

fmt:
    gofmt -w cmd internal

security:
    go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
    npm --prefix docker/browser audit --omit=dev --audit-level=high

build:
    go build -buildvcs=false -trimpath -o bin/hubctl ./cmd/hubctl
    go build -buildvcs=false -trimpath -o bin/hub-runtime ./cmd/runtime
    go build -buildvcs=false -trimpath -o bin/communication-hub ./cmd/communication

release version:
    go run ./cmd/package {{version}}

docker-check target="prod":
    docker build --target {{target}} -t hermes-hub:test -f docker/Dockerfile .
    go run ./cmd/devcheck docker-smoke hermes-hub:test
