FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /p2pool ./cmd/p2pool

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=builder /p2pool /p2pool
RUN mkdir /data

EXPOSE 3333 9171

ENTRYPOINT ["/p2pool", "-data-dir", "/data"]
