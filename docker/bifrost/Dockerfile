FROM golang:1.24 AS builder

ARG BIFROST_REPO="https://github.com/maximhq/bifrost.git"
ARG BIFROST_REF="transports/v1.3.0-prerelease7"

WORKDIR /src

RUN git clone --depth 1 --branch "${BIFROST_REF}" "${BIFROST_REPO}" /src

COPY patches/ /tmp/patches/

RUN if ls /tmp/patches/*.patch >/dev/null 2>&1; then \
      for patch in /tmp/patches/*.patch; do \
        echo "Applying ${patch}" && git apply "${patch}"; \
      done; \
    fi

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go work init ./core ./framework \
        ./plugins/governance \
        ./plugins/logging \
        ./plugins/maxim \
        ./plugins/otel \
        ./plugins/semanticcache \
        ./plugins/telemetry \
        ./transports && \
    cd transports/bifrost-http && \
    rm -rf ui && \
    cp -r ../../ui ./ui && \
    mkdir -p /out && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/bifrost-http ./main.go && \
    rm -rf ui

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl gosu tini \
  && rm -rf /var/lib/apt/lists/*

RUN useradd --system --create-home --home-dir /srv/bifrost --shell /usr/sbin/nologin bifrost

COPY --from=builder /out/bifrost-http /usr/local/bin/bifrost-http
RUN chmod +x /usr/local/bin/bifrost-http

COPY entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/local/bin/entrypoint.sh \
  && mkdir -p /srv/bifrost/app /srv/bifrost/.config/bifrost /srv/bifrost/logs \
  && chown -R bifrost:bifrost /srv/bifrost

ENV BIFROST_HOST=0.0.0.0 \
    BIFROST_PORT=8080 \
    BIFROST_APP_DIR=/srv/bifrost/app \
    BIFROST_USER_CONFIG=/srv/bifrost/.config/bifrost \
    BIFROST_LOG_DIR=/srv/bifrost/logs \
    BIFROST_BINARY=/usr/local/bin/bifrost-http \
    BIFROST_LOG_LEVEL=info \
    BIFROST_LOG_STYLE=json

EXPOSE 8080

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
