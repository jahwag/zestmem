GO_VERSION := 1.25.12
GORELEASER_VERSION := v2.12.7
GOVULNCHECK_VERSION := v1.6.0
SYFT_VERSION := v1.40.0
CONTAINER_RUNTIME ?= podman
CROSS_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: fmt-check verify vulncheck cross-compile integration backup load release-snapshot brand-verify release-verify container-build
fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './dist/*'))"

verify: fmt-check
	go mod tidy -diff
	go vet ./...
	go test -race ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

cross-compile:
	@set -eu; \
	for target in $(CROSS_TARGETS); do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		echo "==> $$os/$$arch"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" go build -trimpath ./cmd/...; \
	done

integration:
	CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) sh ops/test-integration.sh

backup:
	sh ops/test-github-upload.sh
	$(CONTAINER_RUNTIME) build -f Containerfile.backup -t localhost/agent-memory-backup:m1 .
	CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) sh ops/test-backup.sh

load:
	CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) sh ops/test-load.sh

release-snapshot: .tools/syft
	PATH="$(CURDIR)/.tools:$$PATH" go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

.tools/syft:
	mkdir -p .tools
	GOBIN="$(CURDIR)/.tools" go install github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)

container-build:
	$(CONTAINER_RUNTIME) build -f Containerfile -t localhost/zestmem:ci .
	$(CONTAINER_RUNTIME) build -f Containerfile.backup -t localhost/zestmem-backup:ci .

brand-verify:
	sh assets/brand/generate.sh --check

release-verify:
	@test -n "$(VERSION)" || (echo 'usage: make release-verify VERSION=v0.1.0' >&2; exit 2)
	sh scripts/verify-release.sh "$(VERSION)"
