FROM golang:1.21.4-alpine3.18 as builder
WORKDIR /app
COPY . /app

RUN apk --no-cache add make git && make build

FROM alpine:3.18

COPY --from=builder /app/external-dns-porkbun-webhook /
COPY --from=builder /app/docker/entrypoint.sh /
ENTRYPOINT ["/external-dns-porkbun-webhook"]
