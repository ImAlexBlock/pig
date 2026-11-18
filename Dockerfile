# Reproducible image for the daemon. Build from the repo root:
#   docker build -t pig .
# The binary is static (CGO off), so the runtime layer only needs CA roots.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/pig ./cmd/pig

FROM debian:12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1001 pig \
    && useradd -u 1000 -g 1001 -m -d /data -s /usr/sbin/nologin pig
COPY --from=build /out/pig /usr/local/bin/pig
ENV PIG_HOME=/data
WORKDIR /data
USER 1000:1001
VOLUME ["/data"]
# status exits non-zero when the socket is absent, so a dead daemon is unhealthy.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/pig", "daemon", "status"]
ENTRYPOINT ["/usr/local/bin/pig"]
CMD ["daemon", "run", "--foreground"]
