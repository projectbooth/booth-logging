# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/booth-logging ./cmd/logging

# Distroless static + nonroot (uid/gid 65532), matching the chart's podSecurityContext.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/booth-logging /booth-logging
USER nonroot:nonroot
ENTRYPOINT ["/booth-logging"]
