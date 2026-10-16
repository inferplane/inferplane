# syntax=docker/dockerfile:1
# laya-serve on CPU with the fine-tuned Korean PII checkpoint baked in. It binds
# 127.0.0.1:8095, runs as uid 65532, needs no egress and only a writable /tmp.
#   published:  --build-arg WEIGHTS=hf --build-arg HF_REPO=<org>/laya-ko-pii --build-arg HF_REVISION=<commit>
#   local test: --build-arg WEIGHTS=local --build-context localweights=<checkpoint dir>
ARG WEIGHTS=hf
ARG PYTHON_IMAGE=python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1

FROM ${PYTHON_IMAGE} AS weights-hf
ARG HF_REPO
ARG HF_REVISION
COPY SHA256SUMS fetch_weights.py /src/
RUN python /src/fetch_weights.py "$HF_REPO" "$HF_REVISION" /src/SHA256SUMS /weights

FROM ${PYTHON_IMAGE} AS weights-local
COPY SHA256SUMS /src/
COPY --from=localweights . /in/
# Copy only the listed files: a training directory also holds optimizer state.
RUN cd /in && sha256sum -c /src/SHA256SUMS \
 && while read -r _ f; do mkdir -p "/weights/$(dirname "$f")" && cp "$f" "/weights/$f"; done < /src/SHA256SUMS

FROM weights-${WEIGHTS} AS weights

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
