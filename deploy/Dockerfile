# Canopy — Multi-stage Go embed build
#
# Stage 1: Build the binary
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" \
    -o /build/canopyd ./cmd/canopyd

# Stage 2: Minimal runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /build/canopyd /usr/local/bin/canopyd

EXPOSE 8080

ENTRYPOINT ["canopyd"]
CMD ["serve"]
