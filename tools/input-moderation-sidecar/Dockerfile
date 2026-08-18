FROM python:3.12-slim@sha256:2c941e860699f878900b0edc2403613c234d4b32eda3cc9fa7036991a2a63c4a AS builder

ARG MODEL_ID=Qwen/Qwen3Guard-Gen-0.6B
ARG MODEL_REVISION=fada3b2f655b89601929198343c94cd2f64d93cc
ARG MODEL_SAFETENSORS_SHA256=4f3ce47ebd968cddb67de08d8764f8ede7c410a7d1fb9e08145a4c7a2f2e5c0f

ENV PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/models/huggingface

WORKDIR /build

COPY requirements.txt ./
RUN pip install --require-hashes -r requirements.txt

RUN MODEL_ID="${MODEL_ID}" MODEL_REVISION="${MODEL_REVISION}" python -c \
    'import os; from huggingface_hub import snapshot_download; snapshot_download(repo_id=os.environ["MODEL_ID"], revision=os.environ["MODEL_REVISION"], local_dir="/models/qwen3guard")'
RUN echo "${MODEL_SAFETENSORS_SHA256}  /models/qwen3guard/model.safetensors" | sha256sum -c -
RUN printf '%s\n' "${MODEL_REVISION}" > /models/qwen3guard/SAIAI_MODEL_REVISION

COPY app.py test_app.py ./
RUN python -m unittest -v test_app.py

FROM python:3.12-slim@sha256:2c941e860699f878900b0edc2403613c234d4b32eda3cc9fa7036991a2a63c4a

ARG COMMIT=unknown
ARG DATE
ARG SOURCE_URL=https://github.com/yuns2023/saiai-server
ARG MODEL_ID=Qwen/Qwen3Guard-Gen-0.6B
ARG MODEL_REVISION=fada3b2f655b89601929198343c94cd2f64d93cc
ARG MODEL_SAFETENSORS_SHA256=4f3ce47ebd968cddb67de08d8764f8ede7c410a7d1fb9e08145a4c7a2f2e5c0f

LABEL org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      io.saiai.model.id="${MODEL_ID}" \
      io.saiai.model.revision="${MODEL_REVISION}" \
      io.saiai.model.safetensors.sha256="${MODEL_SAFETENSORS_SHA256}"

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models/huggingface \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    TOKENIZERS_PARALLELISM=false \
    MODEL_ID=Qwen/Qwen3Guard-Gen-0.6B \
    MODEL_REVISION=fada3b2f655b89601929198343c94cd2f64d93cc \
    MODEL_PATH=/models/qwen3guard \
    MODEL_DEVICE=cpu \
    OMP_NUM_THREADS=2 \
    MKL_NUM_THREADS=2

RUN groupadd --gid 10001 sidecar && \
    useradd --uid 10001 --gid sidecar --no-create-home --shell /usr/sbin/nologin sidecar

WORKDIR /app

COPY --from=builder /usr/local /usr/local
COPY --from=builder --chown=sidecar:sidecar /models/qwen3guard /models/qwen3guard
COPY --chown=sidecar:sidecar app.py ./

USER sidecar

EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=5s --start-period=180s --retries=5 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/healthz', timeout=3).read()"]

CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8081", "--workers", "1", "--no-access-log"]
