FROM golang:1.25.3-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 go build -o hindsight_auth_proxy -ldflags="-w -s" ./.

FROM gcr.io/distroless/static

WORKDIR /app

COPY --from=builder /app/hindsight_auth_proxy /usr/local/bin/hindsight_auth_proxy

ENTRYPOINT ["/usr/local/bin/hindsight_auth_proxy"]
