# syntax=docker/dockerfile:1.7
# Étapes : build (dépendances) → test (vet + tests) → compile → runtime (image finale minimale)
ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-trixie AS build
# Plugins libheif : dav1d (décodage AVIF rapide), aomenc (encodage AVIF), libde265 (HEIC iPhone)
RUN apt-get update \
 && apt-get install -y --no-install-recommends libvips-dev \
    libheif-plugin-dav1d libheif-plugin-aomenc libheif-plugin-libde265 \
 && rm -rf /var/lib/apt/lists/*
# ONNX Runtime (Microsoft, MIT) : exécute le modèle de visages YuNet. Version alignée sur
# github.com/yalue/onnxruntime_go v1.36 ; archive vérifiée par empreinte SHA-256.
ARG ORT_VERSION=1.29.0
ARG ORT_SHA256=c3fddc4f139a045b0c4902c57410f0694f1c2fdf9b6939fbe38b1aeae7cd14ba
RUN curl -fsSL -o /tmp/ort.tgz https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz \
 && echo "${ORT_SHA256}  /tmp/ort.tgz" | sha256sum -c - \
 && mkdir -p /usr/local/lib/onnxruntime \
 && tar -xzf /tmp/ort.tgz -C /tmp \
 && cp /tmp/onnxruntime-linux-x64-${ORT_VERSION}/lib/libonnxruntime.so.${ORT_VERSION} /usr/local/lib/onnxruntime/libonnxruntime.so \
 && rm -rf /tmp/ort.tgz /tmp/onnxruntime-linux-x64-*
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

FROM build AS test
ARG STATICCHECK_VERSION=v0.8.1
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go install honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test -z "$(gofmt -l .)" || { echo "Fichiers mal formatés :"; gofmt -l .; exit 1; } \
 && go vet ./... \
 && staticcheck -checks "all,-ST1000,-ST1003,-ST1016" ./... \
 && { go test -count=1 -v ./... > /tmp/test.log 2>&1; s=$?; cat /tmp/test.log; exit $s; } \
 && { go test -count=1 -run '^$' -bench . -benchtime 20x ./internal/process/ > /tmp/bench.log 2>&1; s=$?; cat /tmp/bench.log; exit $s; }

FROM build AS compile
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/proxykenny ./cmd/proxykenny

# Web app de démonstration : Go pur (pas de libvips) → image minuscule, sans shell.
FROM build AS compile-lab
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/pklab ./cmd/pklab

FROM gcr.io/distroless/static-debian12 AS lab
COPY --from=compile-lab /out/pklab /pklab
USER 10001
EXPOSE 8090 8091
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 CMD ["/pklab", "-healthcheck"]
ENTRYPOINT ["/pklab"]

FROM debian:trixie-slim AS runtime
# libvips + tous ses chargeurs (HEIF/AVIF, JPEG XL, PDF, SVG, ImageMagick...) ; jemalloc
# limite la fragmentation mémoire des allocations natives de libvips.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libvips42t64 libjemalloc2 ca-certificates \
    libheif-plugin-dav1d libheif-plugin-aomenc libheif-plugin-libde265 \
 && rm -rf /var/lib/apt/lists/* /usr/share/doc /usr/share/man \
 && useradd --system --uid 10001 --no-create-home --shell /usr/sbin/nologin proxykenny
ENV LD_PRELOAD=/usr/lib/x86_64-linux-gnu/libjemalloc.so.2 \
    MALLOC_CONF=background_thread:true,dirty_decay_ms:1000,muzzy_decay_ms:1000 \
    MAGICK_THREAD_LIMIT=1 \
    MAGICK_MEMORY_LIMIT=256MiB \
    MAGICK_DISK_LIMIT=0 \
    KP_BIND=:8080 \
    KP_METRICS_BIND=:8081
COPY --from=build /usr/local/lib/onnxruntime /usr/local/lib/onnxruntime
COPY --from=compile /out/proxykenny /usr/local/bin/proxykenny
USER 10001
EXPOSE 8080 8081
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 CMD ["/usr/local/bin/proxykenny", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/proxykenny"]
