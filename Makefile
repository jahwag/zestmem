GO_VERSION := 1.25.12
GORELEASER_VERSION := v2.12.7
GOVULNCHECK_VERSION := v1.6.0
SYFT_VERSION := v1.40.0
CONTAINER_RUNTIME ?= podman

.PHONY: fmt-check verify integration backup load release-snapshot brand-verify release-verify container-build
fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './dist/*'))"

verify: fmt-check
	go mod tidy
	git diff --exit-code -- go.mod go.sum
	go test ./...
	go vet ./...
	go test -race ./...
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

integration:
	CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) sh ops/test-integration.sh

backup:
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
