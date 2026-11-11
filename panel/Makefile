SHELL := /bin/bash
export PATH := $(CURDIR)/bin/.go/bin:$(CURDIR)/bin:$(PATH)
GO := $(CURDIR)/bin/.go/bin/go
BUF := $(CURDIR)/bin/buf
export GOTOOLCHAIN := local
export GOMAXPROCS := 2
export GOFLAGS := -p=2

.PHONY: bootstrap version-check build generate check check-generated format

bootstrap:
	./scripts/bootstrap-tools.sh

version-check:
	@test "$$( $(GO) env GOVERSION)" = go1.27.3
	@test "$$( $(BUF) --version)" = 1.75.0
	@test "$$(bin/sqlc version)" = v1.31.1
	@test "$$(bin/golangci-lint version --short)" = 2.15.0
	@test "$$(bin/gitleaks version)" = 8.30.1

build: version-check
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/panel ./cmd/panel

.PHONY: web-bootstrap web-assets release
web-bootstrap:
	./scripts/bootstrap-web-tools.sh

web-assets:
	./scripts/build-web-assets.sh

release: web-assets version-check
	CGO_ENABLED=0 $(GO) build -tags=webassets -trimpath -o bin/panel ./cmd/panel

generate: version-check
	$(BUF) generate
	bin/sqlc generate

check: version-check
	@test -z "$$(gofmt -l cmd internal gen/go)"
	$(BUF) format --diff --exit-code
	$(BUF) lint
	bin/sqlc vet
	$(BUF) build -o /dev/null
	$(GO) mod tidy -diff
	$(GO) -C tools mod tidy -diff
	$(GO) build ./...
	$(GO) vet ./...

check-generated: version-check
	./scripts/check-generated.sh
	./scripts/check-sqlc.sh

format: version-check
	gofmt -w cmd internal
	$(BUF) format -w

.PHONY: static-tools lint vulnerabilities secrets

static-tools: version-check
	$(GO) -C tools build -trimpath -o ../bin/nilaway go.uber.org/nilaway/cmd/nilaway
	$(GO) -C tools build -trimpath -o ../bin/govulncheck golang.org/x/vuln/cmd/govulncheck

lint: version-check
	bin/golangci-lint config verify
	bin/golangci-lint run ./...
	bin/nilaway -include-pkgs=github.com/aleksdotbar/vps/panel -exclude-pkgs=github.com/aleksdotbar/vps/panel/gen ./...

vulnerabilities: version-check
	bin/govulncheck ./...

secrets: version-check
	./scripts/check-secrets.py

.PHONY: test test-inspection
test: version-check
	$(GO) test -count=1 ./cmd/... ./internal/...

test-inspection: version-check
	./scripts/test-inspection.sh
