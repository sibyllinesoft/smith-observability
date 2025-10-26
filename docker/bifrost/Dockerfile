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

RUN python3 - <<'PY'
from pathlib import Path
path = Path('/src/plugins/otel/converter.go')
text = path.read_text()

if 'github.com/bytedance/sonic' not in text:
    text = text.replace(
        '\t"github.com/maximhq/bifrost/core/schemas"\n',
        '\t"github.com/bytedance/sonic"\n\t"github.com/maximhq/bifrost/core/schemas"\n',
        1,
    )

text = text.replace('case "response":', 'case "response", "responses":')

marker = '\t\t\tparams = append(params, kvAny("gen_ai.responses.output_messages", arrValue(outputMessages...)))\n'
addition = (
    '\t\t\tvar outputPayload any\n'
    '\t\t\tif resp != nil {\n'
    '\t\t\t\tvar rawPayload map[string]any\n'
    '\t\t\t\tswitch raw := resp.ExtraFields.RawResponse.(type) {\n'
    '\t\t\t\tcase string:\n'
    '\t\t\t\t\tif strings.TrimSpace(raw) != "" {\n'
    '\t\t\t\t\t\tif err := sonic.Unmarshal([]byte(raw), &rawPayload); err != nil {\n'
    '\t\t\t\t\t\t\trawPayload = nil\n'
    '\t\t\t\t\t\t}\n'
    '\t\t\t\t\t}\n'
    '\t\t\t\tcase []byte:\n'
    '\t\t\t\t\tif len(raw) > 0 {\n'
    '\t\t\t\t\t\tif err := sonic.Unmarshal(raw, &rawPayload); err != nil {\n'
    '\t\t\t\t\t\t\trawPayload = nil\n'
    '\t\t\t\t\t\t}\n'
    '\t\t\t\t\t}\n'
    '\t\t\t\tcase map[string]any:\n'
    '\t\t\t\t\trawPayload = raw\n'
    '\t\t\t\t}\n'
    '\t\t\t\tif rawPayload != nil {\n'
    '\t\t\t\t\tif responseBody, ok := rawPayload["response"].(map[string]any); ok {\n'
    '\t\t\t\t\t\tif responsesOutput, ok := responseBody["responses"]; ok && responsesOutput != nil {\n'
    '\t\t\t\t\t\t\toutputPayload = responsesOutput\n'
    '\t\t\t\t\t\t} else if responsesOutput, ok := responseBody["output"]; ok && responsesOutput != nil {\n'
    '\t\t\t\t\t\t\toutputPayload = responsesOutput\n'
    '\t\t\t\t\t\t}\n'
    '\t\t\t\t\t}\n'
    '\t\t\t\t}\n'
    '\t\t\t}\n'
    '\t\t\tif outputPayload == nil && resp != nil && resp.ResponsesResponse != nil {\n'
    '\t\t\t\toutputPayload = resp.ResponsesResponse.Output\n'
    '\t\t\t}\n'
    '\t\t\tif outputPayload != nil {\n'
    '\t\t\t\tif rawOutput, err := sonic.Marshal(outputPayload); err == nil {\n'
    '\t\t\t\t\tparams = append(params, kvStr("gen_ai.responses.output_json", string(rawOutput)))\n'
    '\t\t\t\t}\n'
    '\t\t\t}\n'
)
if addition.strip() not in text and marker in text:
    text = text.replace(marker, marker + addition, 1)

path.write_text(text)
PY


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
