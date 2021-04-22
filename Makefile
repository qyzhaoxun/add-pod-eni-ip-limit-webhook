.PHONY: build docker-build docker push clean

PKG := github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook

BINARY ?= add-pod-eni-ip-limit-webhook
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

CONTAINER_BUILD_PATH ?= /go/src/$(PKG)
BIN_PATH ?= ./bin/$(GOOS)/$(GOARCH)/$(BINARY)

REGISTRY ?= ccr.ccs.tencentyun.com/tkeimages
IMAGE ?= $(REGISTRY)/$(BINARY)

VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS ?= -X main.version=$(VERSION)

# Default to build the Linux binary
build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) go build -o $(BIN_PATH) -ldflags "$(LDFLAGS)" ./

docker-build:
	docker run --rm -v $(shell pwd):$(CONTAINER_BUILD_PATH) \
		--workdir=$(CONTAINER_BUILD_PATH) \
		-e GOARCH=$(GOARCH)\
		-e GOOS=$(GOOS)\
		-e CGO_ENABLED=$(CGO_ENABLED)\
		golang:1.14 bash -c "go mod tidy&&make build"

docker: docker-build
	$(eval IMAGE := $(REGISTRY)/$(BINARY):$(GOOS)-$(GOARCH)_$(VERSION))
	$(if $(filter amd64, $(GOARCH)), $(eval BASEIMAGE := amd64/alpine:3.12), $(if $(filter arm64, $(GOARCH)), $(eval BASEIMAGE := arm64v8/alpine:3.12),))
	@docker build --build-arg BASEIMAGE=$(BASEIMAGE) --build-arg GOOS=$(GOOS) --build-arg GOARCH=$(GOARCH) -f Dockerfile -t "$(IMAGE)" .
	@echo "Built Docker image \"$(IMAGE):$(VERSION)\""

push: docker
	$(eval IMAGE := $(REGISTRY)/$(BINARY):$(GOOS)-$(GOARCH)_$(VERSION))
	docker push $(IMAGE)

clean:
	rm -rf bin

buildx:
	@GOARCH=amd64 make push
	@GOARCH=arm64 make push
	$(eval IMAGE := $(REGISTRY)/$(BINARY):$(VERSION))
	$(eval IMAGES := $(foreach arch, amd64 arm64, $(REGISTRY)/$(BINARY):$(GOOS)-$(arch)_$(VERSION)))
	@echo "===========> push multi-arch image $(IMAGE)"
	@docker buildx imagetools create -t $(IMAGE) $(IMAGES)
