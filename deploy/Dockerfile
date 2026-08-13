# Canopy — Multi-stage production build
#
# Stage 1: Build the frontend
FROM node:22-alpine AS frontend-builder

WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci

COPY frontend/ .
RUN npm run build

# Stage 2: Build the Go API binary.
# NOTE: canopyd is API-only in MVP — frontend/dist is built as a release
# artifact for external serving, NOT embedded in the binary.
FROM golang:1.25-alpine AS go-builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend-builder /src/frontend/dist ./frontend/dist

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" \
    -o /build/canopyd ./cmd/canopyd

# Stage 3: Minimal runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=go-builder /build/canopyd /usr/local/bin/canopyd

EXPOSE 8080

ENTRYPOINT ["canopyd"]
CMD ["serve"]
