# DeployPilot build helpers for Linux/macOS/CI.
#
#   make web         build the React UI into client/internal/webui/dist
#   make test        go vet + go test
#   make build       web + cross-compile linux/amd64,arm64 + checksums + install.sh
#   make lint        shellcheck the installer
#   make e2e-docker  smoke-test dist/ inside a clean ubuntu:22.04 container
#   make clean       remove build outputs
#
# Override the version with: make build VERSION=v1.2.3

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
ARCHES  := amd64 arm64

.DEFAULT_GOAL := build
.PHONY: web test build lint e2e-docker clean

web:
	cd web && npm ci && npm run build

test:
	cd client && go vet ./... && go test ./...

build: web
	mkdir -p dist
	@set -e; for arch in $(ARCHES); do \
		echo "building deploypilot-linux-$$arch ($(VERSION)) ..."; \
		( cd client && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ../dist/deploypilot-linux-$$arch ./cmd/deploypilot ); \
	done
	cd dist && sha256sum deploypilot-linux-* > checksums.txt
	cp scripts/install.sh dist/install.sh

lint:
	shellcheck scripts/install.sh

e2e-docker: build
	docker run --rm -v "$(CURDIR)/dist:/dist" ubuntu:22.04 sh -c 'apt-get update -qq && apt-get install -y -qq ca-certificates curl >/dev/null && cd /dist && sha256sum -c checksums.txt && (timeout 30 ./deploypilot-linux-amd64 > /tmp/out.txt 2>&1 || true) && cat /tmp/out.txt && grep -q "trycloudflare.com" /tmp/out.txt'

clean:
	rm -rf dist client/internal/webui/dist/assets
