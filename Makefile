.PHONY: build test clean

GO = CGO_ENABLED=1 GO111MODULE=on go

MICROSERVICES=cmd/edgex-influx-proxy

.PHONY: $(MICROSERVICES)

VERSION=$(shell cat ./VERSION)
GIT_SHA=$(shell git rev-parse HEAD)
GOFLAGS=-ldflags "-X github.com/anonymouse64/edgex-influx-proxy.Version=$(VERSION)"

build: $(MICROSERVICES)
	$(GO) build ./...

cmd/edgex-influx-proxy:
	$(GO) build $(GOFLAGS) -o $@ ./cmd

test:

clean:
	rm -f $(MICROSERVICES)

run: build
	cd cmd && ./edgex-influx-proxy