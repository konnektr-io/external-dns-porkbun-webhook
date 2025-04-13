FROM golang:1.21.4-alpine3.18 AS builder
WORKDIR /app
COPY . /app

RUN apk --no-cache add make git && make build

FROM alpine:3.18

COPY --from=builder /app/external-dns-porkbun-webhook /
ENTRYPOINT ["/external-dns-porkbun-webhook"]
