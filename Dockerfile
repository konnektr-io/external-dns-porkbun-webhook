FROM golang:1.24.2-alpine AS builder
WORKDIR /app
COPY . /app

RUN apk --no-cache add make git && make build

FROM alpine:3.21

COPY --from=builder /app/external-dns-porkbun-webhook /
ENTRYPOINT ["/external-dns-porkbun-webhook"]
