GO ?= go
VERSION ?= dev
LDFLAGS = -s -w -X github.com/jdevera/dotagents/internal/app.Version=$(VERSION)
PLATFORMS = darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

.PHONY: build test vet cross checksums

build:
	@mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/dotagents ./cmd/dotagents

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

cross:
	@mkdir -p dist
	@set -eu; for target in $(PLATFORMS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		printf 'Building %s/%s\n' "$$os" "$$arch"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "dist/dotagents-$$os-$$arch" ./cmd/dotagents; \
	done
	cp THIRD_PARTY_NOTICES.txt dist/THIRD_PARTY_NOTICES.txt
	$(MAKE) checksums

checksums:
	@cd dist && if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum dotagents-darwin-amd64 dotagents-darwin-arm64 dotagents-linux-amd64 dotagents-linux-arm64 > SHA256SUMS; \
	else \
		shasum -a 256 dotagents-darwin-amd64 dotagents-darwin-arm64 dotagents-linux-amd64 dotagents-linux-arm64 > SHA256SUMS; \
	fi
