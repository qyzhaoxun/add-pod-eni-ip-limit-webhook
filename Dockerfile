FROM alpine:3.6

RUN apk add --no-cache ca-certificates

ADD ./bin/add-pod-eni-ip-limit-webhook /webhook
ENTRYPOINT ["/webhook"]