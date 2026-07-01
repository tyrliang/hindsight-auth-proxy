FROM golang:1.25.3-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 go build -o hindsight_auth_proxy -ldflags="-w -s" ./.

FROM alpine:3.20
RUN apk add --no-cache tailscale

WORKDIR /app
COPY --from=builder /app/hindsight_auth_proxy /usr/local/bin/hindsight_auth_proxy
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
