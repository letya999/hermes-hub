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
    go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes= -config-file .github/actionlint.yaml .github/workflows/ci.yml .github/workflows/runner.yml .github/workflows/prune-images.yml

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
    go run ./cmd/devcheck docker-build hermes-hub:test-control {{target}}-control
    docker run --rm --entrypoint docker hermes-hub:test-control --version
    go run ./cmd/devcheck docker-smoke hermes-hub:test
    go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test
    go run -tags integration ./cmd/devcheck hermes-capability-contract hermes-hub:test
    go run -tags integration ./cmd/devcheck managed-network-canary hermes-hub:test
    go run -tags integration ./cmd/devcheck managed-supervisor-canary hermes-hub:test
    go run -tags integration ./cmd/devcheck scratch-workload-canary hermes-hub:test
    HUB_SCRATCH_FAULT_IMAGE=hermes-hub:test go test -tags integration -run TestDockerScratchExecPausedQuarantine ./internal/toolhub
    go run ./cmd/devcheck docker-clean

# Pull the CI-built GHCR image for HEAD (edge-<target> fallback) and retag it
# locally. Requires `docker login ghcr.io` once on this machine.
docker-pull target="prod":
    go run ./cmd/devcheck docker-pull hermes-hub:test {{target}}
    go run ./cmd/devcheck docker-pull hermes-hub:test-control {{target}}-control

# Same gate as docker-check but on the pulled image: no local build stage.
docker-check-prebuilt target="prod": (docker-pull target)
    docker run --rm --entrypoint docker hermes-hub:test-control --version
    go run ./cmd/devcheck docker-smoke hermes-hub:test
    go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test
    go run -tags integration ./cmd/devcheck hermes-capability-contract hermes-hub:test
    go run -tags integration ./cmd/devcheck managed-network-canary hermes-hub:test
    go run -tags integration ./cmd/devcheck managed-supervisor-canary hermes-hub:test
    go run -tags integration ./cmd/devcheck scratch-workload-canary hermes-hub:test
    HUB_SCRATCH_FAULT_IMAGE=hermes-hub:test go test -tags integration -run TestDockerScratchExecPausedQuarantine ./internal/toolhub
    go run ./cmd/devcheck docker-clean

# Probe the pinned upstream without rebuilding or accessing user/provider data.
capability-check image="hermes-hub:test":
    go run -tags integration ./cmd/devcheck hermes-capability-contract {{image}}

# Reclaim superseded hermes-hub tags, dangling images and orphan build
# resources; normal mode keeps up to 8 GB of BuildKit cache. Deep drops it.
docker-clean flag="":
    go run ./cmd/devcheck docker-clean {{flag}}

# Credential Broker is a separate Go module and process. Keep its own gates
# intact while making the monorepo entrypoint explicit.
credential-broker-check:
    just --justfile services/credential-broker/justfile --working-directory services/credential-broker check

credential-broker-build:
    just --justfile services/credential-broker/justfile --working-directory services/credential-broker build

credential-broker-docker-check:
    docker build --file services/credential-broker/deploy/Dockerfile --tag hermes-credential-broker:test services/credential-broker
