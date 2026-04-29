.PHONY: build install test test-short lint vet clean fmt

GOFLAGS_TEST := -shuffle=on

build:
	go build ./...

install:
	GOBIN=$${HOME}/.local/bin go install ./cmd/kata

test:
	go test $(GOFLAGS_TEST) ./...

test-short:
	go test -short $(GOFLAGS_TEST) ./...

lint:
	golangci-lint run --config .golangci.yml

lint-ci:
	golangci-lint run --config .golangci.yml --no-fix

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f kata kata.exe coverage.out
	rm -rf dist
