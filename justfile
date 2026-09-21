set shell := ["bash", "-euo", "pipefail", "-c"]
set windows-shell := ["powershell.exe", "-NoLogo", "-NoProfile", "-Command"]

# Force BuildKit/Bake for every docker invocation through just: the classic
# builder materializes each stage as cache images and duplicates the full image
# size on every rebuild, filling the Docker Desktop disk.
export DOCKER_BUILDKIT := "1"
export COMPOSE_DOCKER_CLI_BUILD := "1"
export COMPOSE_BAKE := "true"

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
    go build -buildvcs=false -trimpath -o bin/toolhub ./cmd/toolhub
    go build -buildvcs=false -trimpath -o bin/communication-hub ./cmd/communication
    go build -buildvcs=false -trimpath -o bin/hub-supervisor ./cmd/supervisor

release version:
    go run ./cmd/package {{version}}

docker-check target="prod":
    go run ./cmd/devcheck docker-build hermes-hub:test {{target}}
    go run ./cmd/devcheck docker-smoke hermes-hub:test
    go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test
    go run ./cmd/devcheck docker-clean

# Reclaim superseded hermes-hub tags, dangling images and orphan build
# resources; "just docker-clean --deep" also drops the shared BuildKit cache.
docker-clean flag="":
    go run ./cmd/devcheck docker-clean {{flag}}

# Credential Broker is a separate Go module and process. Keep its own gates
# intact while making the monorepo entrypoint explicit.
credential-broker-check:
    just --working-directory services/credential-broker check

credential-broker-build:
    just --working-directory services/credential-broker build

credential-broker-docker-check:
    docker build --file services/credential-broker/deploy/Dockerfile --tag hermes-credential-broker:test services/credential-broker
