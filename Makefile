# Entry points for everything CI runs, so `make check` locally and a green
# pipeline mean the same thing. Go targets run at the repository root; the
# web targets shell into web/.

BIN_DIR    := bin
BINARY     := $(BIN_DIR)/orrery
PKG        := ./cmd/orrery
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X main.version=$(VERSION)
DEV_CONFIG ?= configs/orrery.dev.yaml

.DEFAULT_GOAL := help

##@ General

help: ## List the available targets
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Go

build: ## Build the server binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)

run: ## Run the server against the dev config
	go run $(PKG) -config $(DEV_CONFIG)

test: ## Run the Go tests with the race detector
	go test ./... -race -count=1

cover: ## Run the tests with coverage and report the total
	go test ./... -race -count=1 -covermode=atomic -coverprofile=coverage.out
	@go tool cover -func=coverage.out | awk '/^total:/ { print "total statement coverage: " $$3 }'

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy and verify the module graph
	go mod tidy
	go mod verify

bundle: web-build ## Build a self-contained binary with the web UI embedded
	rm -rf internal/webfs/dist
	cp -R web/dist internal/webfs/dist
	CGO_ENABLED=0 go build -trimpath -tags bundleweb -ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)

##@ Web

web-install: ## Install the web dependencies from the lockfile
	cd web && npm ci

web-dev: ## Start the Vite dev server
	cd web && npm run dev

web-build: ## Build the production bundle
	cd web && npm run build

web-test: ## Run the web unit tests
	cd web && npm test

web-lint: ## Lint the web app
	cd web && npm run lint

web-typecheck: ## Typecheck the web app (tsc -b, not --noEmit)
	cd web && npx tsc -b

##@ Packaging

image: ## Build the container image locally
	docker build -t orrery:dev .

helm-lint: ## Lint and render the Helm chart
	helm lint deploy/helm/orrery
	helm template orrery deploy/helm/orrery \
		--set publicURL=https://orrery.example.com \
		--set oidc.issuer=https://accounts.example.com \
		--set oidc.clientID=orrery > /dev/null

##@ Meta

check: vet test web-typecheck web-lint web-test web-build helm-lint ## Everything CI gates on

clean: ## Remove build output and coverage profiles
	rm -rf $(BIN_DIR) coverage.out coverage.html internal/webfs/dist

.PHONY: help build run test cover vet tidy bundle \
	web-install web-dev web-build web-test web-lint web-typecheck \
	image helm-lint check clean
