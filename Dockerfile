# syntax=docker/dockerfile:1

# Build stage: compile the gateway binary with module caching.
FROM golang:1.27.2-alpine AS build

WORKDIR /src

# Cache module downloads separately from source edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Strip DWARF and the symbol table to shrink the binary, and disable CGO for a
# static build that runs on a distroless base.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/gateway \
    ./cmd/gateway

# Runtime stage: minimal non-root image.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/gateway /gateway

# Cloud Run injects the port; default to 8080 to match local Compose.
ENV GATEWAY_PORT=8080
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/gateway"]
