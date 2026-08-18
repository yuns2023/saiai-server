import asyncio
import logging
import os
import re
from contextlib import asynccontextmanager
from pathlib import Path

import torch
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from transformers import AutoModelForCausalLM, AutoTokenizer


MODEL_ID = os.getenv("MODEL_ID", "Qwen/Qwen3Guard-Gen-0.6B").strip()
MODEL_REVISION = os.getenv("MODEL_REVISION", "fada3b2f655b89601929198343c94cd2f64d93cc").strip()
MODEL_PATH = Path(os.getenv("MODEL_PATH", "/models/qwen3guard").strip())
MODEL_VERSION = f"{MODEL_ID}@{MODEL_REVISION}"
MODEL_DEVICE = os.getenv("MODEL_DEVICE", "auto").strip()
MAX_INPUT_CHARS = int(os.getenv("MAX_INPUT_CHARS", "32768"))
MAX_CHUNK_TOKENS = int(os.getenv("MAX_CHUNK_TOKENS", "3072"))
CHUNK_OVERLAP_TOKENS = int(os.getenv("CHUNK_OVERLAP_TOKENS", "128"))
MAX_CHUNKS = int(os.getenv("MAX_CHUNKS", "8"))
MAX_NEW_TOKENS = int(os.getenv("MAX_NEW_TOKENS", "64"))
MAX_CONCURRENCY = int(os.getenv("MAX_CONCURRENCY", "1"))

SAFETY_PATTERN = re.compile(r"Safety:\s*(Safe|Unsafe|Controversial)", re.IGNORECASE)
CATEGORY_PATTERN = re.compile(
    r"Violent|Non-violent Illegal Acts|Sexual Content or Sexual Acts|PII|"
    r"Suicide\s*&\s*Self-Harm|Unethical Acts|Politically Sensitive Topics|"
    r"Copyright Violation|Jailbreak|None",
    re.IGNORECASE,
)
CATEGORY_LINE_PATTERN = re.compile(r"^Categories:\s*(.+)$", re.IGNORECASE | re.MULTILINE)
SAFETY_RANK = {"Safe": 0, "Controversial": 1, "Unsafe": 2}

tokenizer = None
model = None
inference_slots = asyncio.Semaphore(max(1, MAX_CONCURRENCY))
log = logging.getLogger("saiai.input_moderation")


class ClassifyRequest(BaseModel):
    text: str = Field(min_length=1, max_length=MAX_INPUT_CHARS)


class ClassifyResponse(BaseModel):
    safety: str
    categories: list[str]
    model_version: str


def canonical_safety(value: str) -> str:
    return value[:1].upper() + value[1:].lower()


def canonical_category(value: str) -> str:
    normalized = re.sub(r"\s+", " ", value.strip()).lower()
    categories = {
        "violent": "Violent",
        "non-violent illegal acts": "Non-violent Illegal Acts",
        "sexual content or sexual acts": "Sexual Content or Sexual Acts",
        "pii": "PII",
        "suicide & self-harm": "Suicide & Self-Harm",
        "unethical acts": "Unethical Acts",
        "politically sensitive topics": "Politically Sensitive Topics",
        "copyright violation": "Copyright Violation",
        "jailbreak": "Jailbreak",
        "none": "None",
    }
    return categories.get(normalized, value.strip())


def parse_model_output(output: str) -> tuple[str, list[str]]:
    safety_match = SAFETY_PATTERN.search(output)
    if safety_match is None:
        raise ValueError("model response omitted Safety label")
    safety = canonical_safety(safety_match.group(1))
    categories: list[str] = []
    seen: set[str] = set()
    category_line = CATEGORY_LINE_PATTERN.search(output)
    category_text = category_line.group(1) if category_line is not None else ""
    for match in CATEGORY_PATTERN.finditer(category_text):
        category = canonical_category(match.group(0))
        if category == "None" or category in seen:
            continue
        seen.add(category)
        categories.append(category)
    return safety, categories


def split_text(text: str) -> list[str]:
    token_ids = tokenizer.encode(text, add_special_tokens=False)
    if not token_ids:
        return []
    chunk_size = max(256, MAX_CHUNK_TOKENS)
    overlap = min(max(0, CHUNK_OVERLAP_TOKENS), chunk_size - 1)
    step = chunk_size - overlap
    chunks: list[str] = []
    for start in range(0, len(token_ids), step):
        chunk_ids = token_ids[start : start + chunk_size]
        if not chunk_ids:
            break
        chunks.append(tokenizer.decode(chunk_ids, skip_special_tokens=True))
        if len(chunks) >= max(1, MAX_CHUNKS) or start + chunk_size >= len(token_ids):
            break
    return chunks


def classify_chunk(text: str) -> tuple[str, list[str]]:
    rendered = tokenizer.apply_chat_template(
        [{"role": "user", "content": text}],
        tokenize=False,
    )
    model_inputs = tokenizer([rendered], return_tensors="pt").to(model.device)
    with torch.inference_mode():
        generated_ids = model.generate(
            **model_inputs,
            max_new_tokens=max(16, MAX_NEW_TOKENS),
            do_sample=False,
        )
    output_ids = generated_ids[0][len(model_inputs.input_ids[0]) :].tolist()
    output = tokenizer.decode(output_ids, skip_special_tokens=True)
    return parse_model_output(output)


def classify(text: str) -> ClassifyResponse:
    chunks = split_text(text[:MAX_INPUT_CHARS])
    if not chunks:
        raise ValueError("text is empty")
    final_safety = "Safe"
    final_categories: list[str] = []
    seen: set[str] = set()
    for chunk in chunks:
        safety, categories = classify_chunk(chunk)
        if SAFETY_RANK[safety] > SAFETY_RANK[final_safety]:
            final_safety = safety
        for category in categories:
            if category not in seen:
                seen.add(category)
                final_categories.append(category)
    return ClassifyResponse(
        safety=final_safety,
        categories=final_categories,
        model_version=MODEL_VERSION,
    )


@asynccontextmanager
async def lifespan(_: FastAPI):
    global tokenizer, model
    if not MODEL_PATH.is_dir():
        raise RuntimeError("embedded model directory is missing")
    tokenizer = AutoTokenizer.from_pretrained(MODEL_PATH, local_files_only=True)
    model_kwargs = {"dtype": "auto", "low_cpu_mem_usage": True, "local_files_only": True}
    model_kwargs["device_map"] = MODEL_DEVICE or "auto"
    model = AutoModelForCausalLM.from_pretrained(MODEL_PATH, **model_kwargs)
    model.eval()
    yield
    tokenizer = None
    model = None
    if torch.cuda.is_available():
        torch.cuda.empty_cache()


app = FastAPI(title="SAIAI Input Moderation Sidecar", version="1", lifespan=lifespan)


@app.get("/livez")
async def livez() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    if tokenizer is None or model is None:
        raise HTTPException(status_code=503, detail="model is not ready")
    return {"status": "ok", "model_version": MODEL_VERSION}


@app.post("/v1/classify", response_model=ClassifyResponse)
async def classify_endpoint(request: ClassifyRequest) -> ClassifyResponse:
    text = request.text.strip()
    if not text:
        raise HTTPException(status_code=422, detail="text is empty")
    async with inference_slots:
        try:
            return await asyncio.to_thread(classify, text)
        except (RuntimeError, ValueError) as exc:
            log.exception("classification failed")
            raise HTTPException(status_code=502, detail="classification failed") from exc
