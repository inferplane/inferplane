# syntax=docker/dockerfile:1
# laya-serve on CPU with your fine-tuned Korean PII checkpoint baked in. It binds
# 127.0.0.1:8095, runs as uid 65532, needs no egress and only a writable /tmp.
# No weights or image are distributed: train a checkpoint with ../training/, then
#   docker build --build-context localweights=<checkpoint dir> -t laya-ko-pii:local .
ARG PYTHON_IMAGE=python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1

FROM --platform=$BUILDPLATFORM ${PYTHON_IMAGE} AS weights
COPY weights.txt /src/
COPY --from=localweights . /in/
# Copy only the listed files: a training directory also holds optimizer state.
RUN cd /in && while read -r f; do [ -n "$f" ] || continue; mkdir -p "/weights/$(dirname "$f")" && cp "$f" "/weights/$f" || exit 1; done < /src/weights.txt

FROM ${PYTHON_IMAGE}
ARG TARGETARCH
COPY requirements-amd64.txt requirements-arm64.txt /tmp/req/
RUN pip install --no-cache-dir --require-hashes --no-deps -r /tmp/req/requirements-${TARGETARCH}.txt \
 && rm -rf /tmp/req \
 && useradd --uid 65532 --no-create-home --shell /usr/sbin/nologin laya
COPY --from=weights /weights /models/laya-ko-pii
USER 65532
ENV HOME=/tmp HF_HOME=/tmp/hf HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 \
    LAYA_HOST=127.0.0.1 LAYA_PORT=8095 LAYA_DEVICE=cpu \
    LAYA_MODELS=multilingual LAYA_DEFAULT_MODEL=multilingual \
    LAYA_EXTRA_MODELS='{"multilingual": "/models/laya-ko-pii"}'
ENTRYPOINT ["laya-serve"]
