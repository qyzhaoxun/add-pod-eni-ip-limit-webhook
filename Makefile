.PHONY: build docker-build docker push clean

PKG := github.com/qyzhaoxun/tke-eni-webhook-admission-controller

BINARY ?= tke-eni-webhook-admission-controller

CONTAINER_BUILD_PATH ?= /go/src/$(PKG)
BIN_PATH ?= ./bin/$(BINARY)

REGISTRY ?= ccr.ccs.tencentyun.com/tke-cni
IMAGE ?= $(REGISTRY)/$(BINARY)

#VERSION ?= $(shell git describe --tags --always --dirty)
VERSION ?= v0.0.1
LDFLAGS ?= -X main.version=$(VERSION)

# Default to build the Linux binary
build:
	GOOS=linux CGO_ENABLED=0 go build -o $(BIN_PATH) -ldflags "$(LDFLAGS)" ./

docker-build:
	docker run --rm -v $(shell pwd):$(CONTAINER_BUILD_PATH) \
		--workdir=$(CONTAINER_BUILD_PATH) \
		golang:1.10 make build

docker: docker-build
	@docker build -f Dockerfile -t "$(IMAGE):$(VERSION)" .
	@echo "Built Docker image \"$(IMAGE):$(VERSION)\""

push: docker
	docker push "$(IMAGE):$(VERSION)"

clean:
	rm -rf bin
