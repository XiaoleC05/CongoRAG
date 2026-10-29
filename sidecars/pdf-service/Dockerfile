# PDF 学术解析 sidecar（参赛版）
#
# 【为什么单独一个镜像】解析依赖 PyMuPDF + RapidOCR（torch-free 的 onnx
# 版本），和 Go 镜像的构建链路完全不同；放同一个镜像里会让 api 镜像多出
# 几百 MB 的 Python 依赖。sidecar 通过 HTTP 契约被 worker 调用，见
# docs/sse-protocol.md 与 internal/pdfparse 的注释。
FROM python:3.12-slim

WORKDIR /app

# OCR 依赖需要 libGL/libglib（onnxruntime 的 python 包不打包它们）。
RUN apt-get update \
    && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt requirements-ocr.txt ./
RUN pip install --no-cache-dir -r requirements.txt -r requirements-ocr.txt

COPY parser.py server.py ./

# 容器内必须绑 0.0.0.0：绑回环的话 compose 网络里的 worker 连不进来。
ENV PDF_SERVICE_HOST=0.0.0.0 \
    PDF_SERVICE_PORT=8000

EXPOSE 8000

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
    CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=3).status==200 else 1)"

CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000"]
