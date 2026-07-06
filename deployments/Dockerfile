# Multi-stage build: a static, CGO-free binary, then a minimal distroless
# runtime that runs as a nonroot user.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /leash ./cmd/leash
# Prepare a data dir owned by the distroless nonroot uid so the volume is writable.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /leash /leash
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
EXPOSE 8088 9090
# serve requires an auth token: run with -e LEASH_AUTH_TOKEN=... (or add
# --insecure for a trusted local socket). The admin listener backs the
# healthcheck below; /healthz needs no credential.
ENTRYPOINT ["/leash"]
CMD ["serve", "--listen", ":8088", "--admin", ":9090", "--db", "/data/leash.db"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD ["/leash", "healthcheck", "--url", "http://127.0.0.1:9090/healthz"]
