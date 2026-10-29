"""e2e_test.py - 对已启动的 pdf-service 做端到端实测。

用法：
    python scripts/e2e_test.py --base-url http://127.0.0.1:8077
可选：
    --only v1|v1_img|v7|udl    只测单个用例
    --skip-ocr-acc             跳过 OCR 准确率对比

产物：samples/*.json、samples/summary.json；控制台打印指标表。
"""
from __future__ import annotations

import argparse
import difflib
import json
import re
import sys
import time
import unicodedata
import urllib.error
import urllib.request
import uuid
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]          # _dev/pdf-service
WORKSPACE = ROOT.parents[1]                          # 传智杯/AI创新应用
TEST_DATA = WORKSPACE / "测试数据" / "pdf"
SAMPLES = ROOT / "samples"

DOCS = {
    "v1": {"file": "1706.03762v1.pdf", "ocr": "auto", "max_pages": None,
           "sample": "1706.03762v1.json"},
    "v1_img": {"file": "1706.03762v1_img.pdf", "ocr": "auto", "max_pages": None,
               "sample": "1706.03762v1_img.json"},
    "v7": {"file": "1706.03762v7.pdf", "ocr": "auto", "max_pages": None,
           "sample": "1706.03762v7.json"},
    "udl": {"file": "UnderstandingDeepLearning_02_09_26_C.pdf", "ocr": "auto", "max_pages": 20,
            "sample": "UnderstandingDeepLearning_p20.json"},
}

# 非 TOC 但可以解释的标题（论文标题 / Abstract / References / 附录小节 / 书封面 / 章标签）
ALLOW_EXTRA = {
    "attention is all you need", "abstract", "references", "attention visualizations",
    "understanding deep learning", "chapter 1", "contents", "acknowledgments",
    "preface", "index", "bibliography", "notation", "attribution",
}


def post_pdf(base_url: str, path: Path, ocr: str = "auto", max_pages: int | None = None,
             timeout: float = 1800.0) -> tuple[dict, dict, float]:
    boundary = "----pdfservice" + uuid.uuid4().hex
    filename = path.name
    parts = [
        f"--{boundary}\r\n".encode(),
        f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'.encode(),
        b"Content-Type: application/pdf\r\n\r\n",
        path.read_bytes(),
        f"\r\n--{boundary}--\r\n".encode(),
    ]
    body = b"".join(parts)
    query = [f"ocr={ocr}"]
    if max_pages:
        query.append(f"max_pages={max_pages}")
    url = f"{base_url.rstrip('/')}/parse?" + "&".join(query)
    req = urllib.request.Request(url, data=body, headers={
        "Content-Type": f"multipart/form-data; boundary={boundary}"})
    started = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
        headers = {k.lower(): v for k, v in resp.headers.items()}
    return json.loads(raw.decode("utf-8")), headers, time.perf_counter() - started


def summarize(payload: dict, wall_s: float, headers: dict) -> dict:
    blocks = payload.get("blocks", [])
    types = Counter(b.get("type") for b in blocks)
    headings = [b for b in blocks if b.get("type") == "heading"]
    return {
        "filename": payload.get("filename"),
        "doc_id": payload.get("doc_id", "")[:16],
        "page_count": payload.get("page_count"),
        "pages": payload.get("stats", {}).get("pages_parsed"),
        "wall_s": round(wall_s, 2),
        "parse_ms": payload.get("stats", {}).get("elapsed_ms"),
        "blocks": len(blocks),
        "block_types": dict(sorted(types.items())),
        "headings": len(headings),
        "tables": types.get("table", 0),
        "formulas": types.get("formula", 0),
        "captions": types.get("caption", 0),
        "lists": types.get("list", 0),
        "references": len(payload.get("references", [])),
        "outline_entries": len(payload.get("outline", [])),
        "ocr_pages": payload.get("stats", {}).get("ocr_pages"),
        "cache": headers.get("x-pdf-cache"),
        "ocr_header": headers.get("x-pdf-ocr"),
    }


def normalize_title(text: str) -> str:
    t = unicodedata.normalize("NFKC", text).lower()
    t = re.sub(r"^\s*\d{1,2}(?:\.\d{1,2}){0,2}\.?\s+", "", t)  # 去掉 1 / 1.1 / 1.1.1 编号
    return re.sub(r"[^\w\u4e00-\u9fff]+", "", t)


def title_match(a: str, b: str) -> bool:
    na, nb = normalize_title(a), normalize_title(b)
    if not na or not nb:
        return False
    if na == nb:
        return True
    return difflib.SequenceMatcher(None, na, nb, autojunk=False).ratio() >= 0.85


def load_toc(pdf_path: Path) -> list[dict]:
    import pymupdf
    doc = pymupdf.open(str(pdf_path))
    toc = [{"title": str(t).strip(), "level": int(lv), "page": int(pg)}
           for lv, t, pg in doc.get_toc() if str(t).strip()]
    doc.close()
    return toc


def toc_eval(payload: dict, pdf_path: Path) -> dict:
    pages_parsed = payload.get("stats", {}).get("pages_parsed", 0)
    toc = load_toc(pdf_path)
    toc_in = [e for e in toc if 0 < e["page"] <= pages_parsed]
    headings = [b for b in payload["blocks"] if b["type"] == "heading"]
    matched = [e for e in toc_in
               if any(title_match(e["title"], b["text"]) for b in headings)]
    extras = [b["text"] for b in headings
              if not any(title_match(b["text"], e["title"]) for e in toc)
              and normalize_title(b["text"]) not in ALLOW_EXTRA]
    return {
        "toc_entries_in_range": len(toc_in),
        "toc_matched": len(matched),
        "toc_recall": round(len(matched) / len(toc_in), 3) if toc_in else None,
        "extracted_headings": len(headings),
        "extra_headings": extras[:20],
        "extra_count": len(extras),
        "missing": [e["title"] for e in toc_in
                    if not any(title_match(e["title"], b["text"]) for b in headings)][:20],
    }


TEXT_TYPES = {"paragraph", "heading", "list", "caption", "reference", "table", "formula"}


def page_texts(payload: dict) -> dict[int, str]:
    by_page: dict[int, list[str]] = {}
    for b in payload["blocks"]:
        if b.get("type") not in TEXT_TYPES:
            continue
        t = (b.get("text") or "").strip()
        if not t:
            continue
        by_page.setdefault(int(b["page"]), []).append(t)
    return {p: "\n".join(v) for p, v in by_page.items()}


def norm_text(text: str, drop_space: bool = False) -> str:
    t = unicodedata.normalize("NFKC", text)
    t = t.replace("-\n", "").replace("\n", " ")
    t = re.sub(r"\s+", "" if drop_space else " ", t).strip().lower()
    return t


def ocr_accuracy(gt_payload: dict, hyp_payload: dict, gt_pdf: Path | None = None) -> dict:
    gt = page_texts(gt_payload)
    hyp = page_texts(hyp_payload)
    common = sorted(set(gt) & set(hyp))
    figure_pages: set[int] = set()
    graphic_pages: set[int] = set()
    if gt_pdf is not None:
        try:
            import pymupdf
            doc = pymupdf.open(str(gt_pdf))
            for i in range(doc.page_count):
                page = doc[i]
                if page.get_images(full=False):
                    figure_pages.add(i + 1)
                # 位图或大量矢量图（图表/示意图/可视化页）视为"图形页"，OCR 与文本层不可直接比较
                if page.get_images(full=False) or len(page.get_drawings()) > 5:
                    graphic_pages.add(i + 1)
            doc.close()
        except Exception:
            figure_pages = set()
            graphic_pages = set()
    per_page = []
    total_gt = 0
    total_ok = 0
    word_ratios = []
    for p in common:
        g = norm_text(gt[p], drop_space=True)
        h = norm_text(hyp[p], drop_space=True)
        if not g or not h:
            continue
        r = difflib.SequenceMatcher(None, g, h, autojunk=False).ratio()
        per_page.append({"page": p, "char_acc": round(r, 4),
                         "gt_chars": len(g), "ocr_chars": len(h)})
        total_gt += len(g)
        total_ok += round(r * len(g))
        gw = norm_text(gt[p]).split()
        hw = norm_text(hyp[p]).split()
        word_ratios.append(difflib.SequenceMatcher(None, gw, hw, autojunk=False).ratio())
    gt_chars = sum(x["gt_chars"] for x in per_page)
    ocr_chars = sum(x["ocr_chars"] for x in per_page)
    body = [x for x in per_page if x["page"] not in graphic_pages]
    body_gt = sum(x["gt_chars"] for x in body)
    body_ok = sum(round(x["char_acc"] * x["gt_chars"]) for x in body)
    return {
        "pages_compared": len(per_page),
        "figure_pages": sorted(figure_pages),
        "char_accuracy_space_insensitive": round(total_ok / total_gt, 4) if total_gt else None,
        "char_accuracy_page_avg": round(sum(x["char_acc"] for x in per_page) / len(per_page), 4)
        if per_page else None,
        "char_accuracy_body_pages": round(body_ok / body_gt, 4) if body_gt else None,
        "body_pages": [x["page"] for x in body],
        "graphic_pages": sorted(graphic_pages),
        "word_accuracy_page_avg": round(sum(word_ratios) / len(word_ratios), 4)
        if word_ratios else None,
        "gt_chars": gt_chars,
        "ocr_chars": ocr_chars,
        "length_ratio": round(ocr_chars / gt_chars, 3) if gt_chars else None,
        "per_page": per_page,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:8000")
    ap.add_argument("--only", default=None, choices=sorted(DOCS))
    ap.add_argument("--skip-ocr-acc", action="store_true")
    args = ap.parse_args()

    SAMPLES.mkdir(parents=True, exist_ok=True)
    health_url = args.base_url.rstrip("/") + "/healthz"
    with urllib.request.urlopen(health_url, timeout=30) as resp:
        health = json.loads(resp.read().decode("utf-8"))
    print("healthz:", json.dumps(health, ensure_ascii=False))

    keys = [args.only] if args.only else list(DOCS)
    summary: dict = {"service": health, "cases": {}, "started_at": time.strftime("%Y-%m-%d %H:%M:%S")}
    summary_path = SAMPLES / "summary.json"
    if args.only and summary_path.exists():
        try:
            old_summary = json.loads(summary_path.read_text(encoding="utf-8"))
            summary["cases"] = old_summary.get("cases", {})
            for k in ("ocr_accuracy", "cache_test"):
                if k in old_summary:
                    summary[k] = old_summary[k]
        except Exception:
            pass
    payloads: dict[str, dict] = {}

    for key in keys:
        cfg = DOCS[key]
        pdf_path = TEST_DATA / cfg["file"]
        if not pdf_path.exists():
            print(f"[SKIP] missing {pdf_path}")
            continue
        print(f"\n=== {key}: {cfg['file']} (ocr={cfg['ocr']}, max_pages={cfg['max_pages']}) ===")
        payload, headers, wall = post_pdf(args.base_url, pdf_path, cfg["ocr"], cfg["max_pages"])
        payloads[key] = payload
        (SAMPLES / cfg["sample"]).write_text(
            json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")
        case = summarize(payload, wall, headers)
        case["toc"] = toc_eval(payload, pdf_path)
        summary["cases"][key] = case
        print(json.dumps(case, ensure_ascii=False, indent=1)[:2500])

    # 缓存命中测试：重复请求 v1
    if "v1" in payloads:
        print("\n=== cache hit test (repeat v1) ===")
        _payload, headers, wall = post_pdf(
            args.base_url, TEST_DATA / DOCS["v1"]["file"], "auto", None)
        summary["cache_test"] = {
            "x_pdf_cache": headers.get("x-pdf-cache"),
            "wall_s": round(wall, 2),
            "parse_ms": headers.get("x-pdf-elapsed-ms"),
        }
        print(json.dumps(summary["cache_test"], ensure_ascii=False))

    if not args.skip_ocr_acc and "v1" in payloads and "v1_img" in payloads:
        print("\n=== OCR accuracy (v1 text layer vs v1_img OCR) ===")
        acc = ocr_accuracy(payloads["v1"], payloads["v1_img"], TEST_DATA / DOCS["v1"]["file"])
        summary["ocr_accuracy"] = acc
        print(json.dumps({k: v for k, v in acc.items() if k != "per_page"}, ensure_ascii=False))

    if not args.only or not (SAMPLES / "summary.json").exists():
        (SAMPLES / "summary.json").write_text(
            json.dumps(summary, ensure_ascii=False, indent=1), encoding="utf-8")
    else:
        if "ocr_accuracy" not in summary:
            summary.pop("ocr_accuracy", None)
        (SAMPLES / "summary.json").write_text(
            json.dumps(summary, ensure_ascii=False, indent=1), encoding="utf-8")
    print(f"\nsaved samples -> {SAMPLES}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
