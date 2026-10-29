"""server.py - CongoRAG PDF 学术解析 sidecar（FastAPI）。

启动：
    uvicorn server:app --host 127.0.0.1 --port 8077
或双击 run.bat。

接口：
    GET  /healthz   -> {status, version, ocr_available}
    POST /parse     -> multipart file + query ocr=auto|on|off & max_pages
"""
from __future__ import annotations

import logging
import os
import sys
import time
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, File, HTTPException, Query, UploadFile
from fastapi.responses import JSONResponse, PlainTextResponse
from starlette.concurrency import run_in_threadpool

BASE_DIR = Path(__file__).resolve().parent
if str(BASE_DIR) not in sys.path:
    sys.path.insert(0, str(BASE_DIR))

import parser as pdf_parser  # noqa: E402

SERVICE_VERSION = pdf_parser.PARSER_VERSION
HOST = os.environ.get("PDF_SERVICE_HOST", "127.0.0.1")
PORT = int(os.environ.get("PDF_SERVICE_PORT", "8000"))
MAX_UPLOAD_MB = int(os.environ.get("PDF_SERVICE_MAX_UPLOAD_MB", "200"))
MAX_UPLOAD_BYTES = MAX_UPLOAD_MB * 1024 * 1024
MAX_PAGES_LIMIT = int(os.environ.get("PDF_SERVICE_MAX_PAGES_LIMIT", "5000"))

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s [pdf-service] %(message)s",
)
logger = logging.getLogger("pdf-service")

app = FastAPI(
    title="CongoRAG PDF Parse Sidecar",
    version=SERVICE_VERSION,
    description="科研学术 PDF 解析 sidecar v0：文本层 + 版面/章节树/公式/表格/参考文献启发式，"
                "扫描页可选 RapidOCR。仅返回结构化 JSON，不做大模型调用。",
)


@app.get("/healthz")
def healthz() -> dict:
    """健康检查：不触发 OCR 模型加载，只做依赖可用性探测。"""
    return {
        "status": "ok",
        "version": SERVICE_VERSION,
        "ocr_available": pdf_parser.ocr_available(),
    }


@app.get("/", response_class=PlainTextResponse)
def index() -> str:
    return (
        f"pdf-service v{SERVICE_VERSION}\n"
        "GET  /healthz\n"
        f"POST /parse  (multipart file; query: ocr=auto|on|off, max_pages)\n"
        f"cache dir: {pdf_parser.default_cache().root}\n"
    )


@app.post("/parse")
async def parse_endpoint(
    file: UploadFile = File(..., description="待解析 PDF 文件"),
    ocr: str = Query("auto", pattern="^(auto|on|off)$", description="OCR 策略：auto|on|off"),
    max_pages: Optional[int] = Query(
        None, ge=0, le=MAX_PAGES_LIMIT,
        description="只解析前 N 页（0=全部）；大文档试跑用"),
):
    started = time.perf_counter()
    data = await file.read()
    if not data:
        raise HTTPException(status_code=400, detail="empty file")
    if len(data) > MAX_UPLOAD_BYTES:
        raise HTTPException(status_code=413, detail=f"file too large (> {MAX_UPLOAD_MB} MB)")
    filename = Path((file.filename or "upload.pdf").replace("\\", "/")).name or "upload.pdf"
    try:
        payload, meta = await run_in_threadpool(
            pdf_parser.parse_pdf_ex, data, filename, ocr, max_pages)
    except pdf_parser.PdfParseError as exc:
        raise HTTPException(status_code=422, detail=f"parse failed: {exc}") from exc
    except Exception as exc:  # pragma: no cover - 兜底
        logger.exception("unexpected parse error: %s", exc)
        raise HTTPException(status_code=500, detail=f"internal error: {type(exc).__name__}: {exc}") from exc

    wall_ms = int((time.perf_counter() - started) * 1000)
    stats = payload.get("stats", {})
    logger.info(
        "parse ok file=%s pages=%s blocks=%s cache=%s ocr_pages=%s parse_ms=%s wall_ms=%s",
        filename, stats.get("pages_parsed"), stats.get("blocks"),
        "hit" if meta["cache_hit"] else "miss", stats.get("ocr_pages"),
        stats.get("elapsed_ms"), wall_ms,
    )
    headers = {
        "X-PDF-Parser-Version": SERVICE_VERSION,
        "X-PDF-Cache": "hit" if meta["cache_hit"] else "miss",
        "X-PDF-OCR": meta["ocr"],
        "X-PDF-Elapsed-Ms": str(stats.get("elapsed_ms", meta["elapsed_ms"])),
        "X-PDF-Wall-Ms": str(wall_ms),
    }
    return JSONResponse(payload, headers=headers)


def main() -> None:
    import uvicorn
    logger.info("starting on http://%s:%s (cache=%s)", HOST, PORT, pdf_parser.default_cache().root)
    uvicorn.run(app, host=HOST, port=PORT, log_level="info")


if __name__ == "__main__":
    main()
