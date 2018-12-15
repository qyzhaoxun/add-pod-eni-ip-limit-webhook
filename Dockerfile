FROM alpine:3.6

RUN apk add --no-cache ca-certificates

ADD ./bin/tke-eni-webhook-admission-controller /webhook
ENTRYPOINT ["/webhook"]