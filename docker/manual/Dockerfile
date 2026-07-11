FROM golang:1.25.4-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/lcoder ./cmd/lcoder

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/lcoder /usr/local/bin/lcoder

WORKDIR /workspace
ENV TERM=xterm-256color

ENTRYPOINT ["lcoder"]
CMD ["tui"]
