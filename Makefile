VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILD_TIME ?= $(shell date -u +%FT%TZ)
BUILDINFO := github.com/rushteam/beauty/pkg/foundation/buildinfo
LDFLAGS := -s -w -X $(BUILDINFO).version=$(VERSION) -X $(BUILDINFO).commit=$(COMMIT) -X $(BUILDINFO).buildTime=$(BUILD_TIME)

.PHONY: build test vet run docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/getdocs ./cmd/getdocs

test:
	go test -race ./...

vet:
	gofmt -l . | (! grep .)
	go vet ./...

run: build
	./bin/getdocs serve -config getdocs.yaml

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) \
		-t getdocs:$(VERSION) .
