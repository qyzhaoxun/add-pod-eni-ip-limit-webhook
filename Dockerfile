ARG BASEIMAGE
FROM ${BASEIMAGE}
ARG GOOS
ARG GOARCH
ARG BIN_PATH=bin/${GOOS}/${GOARCH}
RUN apk add --no-cache ca-certificates

COPY ${BIN_PATH}/add-pod-eni-ip-limit-webhook /
ENTRYPOINT ["/add-pod-eni-ip-limit-webhook"]