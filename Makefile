# Run `make check` before pushing; CI (.github/workflows/ci.yml) runs the same steps.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.10.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: check fmt tidy vet test lint vuln shellcheck build image

check: fmt tidy vet test lint vuln shellcheck

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	go mod tidy -diff

vet:
	go vet ./...

test:
	go test -race ./...

lint:
	$(GOLANGCI_LINT) run ./...

vuln:
	$(GOVULNCHECK) ./...

shellcheck:
	shellcheck build-image.sh dev-broker.sh

build:
	go build -o bin/ptun ./cmd/ptun
	go build -o bin/tunnel-broker ./cmd/tunnel-broker

image:
	docker build -t tunnel-broker:dev .
