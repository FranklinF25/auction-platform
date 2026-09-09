# Multi-stage build: compile a static binary, ship it on distroless.
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/api ./cmd/server

# Distroless static: no shell, no package manager; migrations are embedded in
# the binary. The nonroot variant runs as the built-in non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /bin/api /api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]
