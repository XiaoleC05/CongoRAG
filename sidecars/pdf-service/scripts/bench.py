"""bench.py - 直接调用 parser 的性能/内存基准（不经 HTTP，适合 541 页全量）。

用法：
    python scripts/bench.py --pdf "测试数据/pdf/UnderstandingDeepLearning_02_09_26_C.pdf" \
        --out ../../samples/UDL_full.json
参数：--max-pages N --ocr auto|on|off --no-cache --no-tables
输出：一行 JSON 指标（含耗时、RSS、块统计）。
"""
from __future__ import annotations

import argparse
import ctypes
import json
import sys
import time
import tracemalloc
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

import parser as P  # noqa: E402


def rss_mb() -> tuple[float, float]:
    """返回 (当前工作集 MB, 峰值工作集 MB)。"""
    if sys.platform != "win32":
        import resource
        rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024
        return rss, rss
    from ctypes import wintypes

    class PMC(ctypes.Structure):
        _fields_ = [
            ("cb", wintypes.DWORD), ("PageFaultCount", wintypes.DWORD),
            ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
            ("QuotaPeakPagedPoolUsage", ctypes.c_size_t), ("QuotaPagedPoolUsage", ctypes.c_size_t),
            ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t), ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
            ("PagefileUsage", ctypes.c_size_t), ("PeakPagefileUsage", ctypes.c_size_t),
        ]

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    psapi = ctypes.WinDLL("psapi", use_last_error=True)
    kernel32.GetCurrentProcess.restype = ctypes.c_void_p
    psapi.GetProcessMemoryInfo.argtypes = [ctypes.c_void_p, ctypes.POINTER(PMC), wintypes.DWORD]
    psapi.GetProcessMemoryInfo.restype = ctypes.c_int

    counters = PMC()
    counters.cb = ctypes.sizeof(PMC)
    ok = psapi.GetProcessMemoryInfo(
        kernel32.GetCurrentProcess(), ctypes.byref(counters), counters.cb)
    if not ok:
        return 0.0, 0.0
    return counters.WorkingSetSize / 1048576, counters.PeakWorkingSetSize / 1048576

def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pdf", required=True)
    ap.add_argument("--max-pages", type=int, default=None)
    ap.add_argument("--ocr", default="auto", choices=["auto", "on", "off"])
    ap.add_argument("--no-cache", action="store_true")
    ap.add_argument("--no-tables", action="store_true")
    ap.add_argument("--out", default=None)
    ap.add_argument("--summary-out", default=None, help="只把指标 JSON 写入该文件")
    ap.add_argument("--head-out", default=None, help="把前 N 个 blocks 的样例写入该文件")
    ap.add_argument("--head-blocks", type=int, default=40)
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    pdf = Path(args.pdf)
    data = pdf.read_bytes()
    rss0, peak0 = rss_mb()
    tracemalloc.start()
    t0 = time.perf_counter()
    payload, _meta = P.parse_pdf_ex(
        data, pdf.name, args.ocr, args.max_pages,
        use_cache=not args.no_cache,
        detect_tables=(False if args.no_tables else None))
    elapsed = time.perf_counter() - t0
    current, peak = tracemalloc.get_traced_memory()
    tracemalloc.stop()
    rss1, peak_rss = rss_mb()

    types = Counter(b["type"] for b in payload["blocks"])
    result = {
        "pdf": pdf.name,
        "size_mb": round(len(data) / 1048576, 2),
        "max_pages": args.max_pages,
        "ocr": args.ocr,
        "elapsed_s": round(elapsed, 2),
        "stats": payload["stats"],
        "blocks": len(payload["blocks"]),
        "block_types": dict(sorted(types.items())),
        "headings": types.get("heading", 0),
        "tables": types.get("table", 0),
        "formulas": types.get("formula", 0),
        "references": len(payload["references"]),
        "outline_entries": len(payload["outline"]),
        "rss_before_mb": round(rss0, 1),
        "rss_after_mb": round(rss1, 1),
        "peak_rss_mb": round(peak_rss, 1),
        "tracemalloc_peak_mb": round(peak / 1048576, 1),
    }
    if args.out:
        out = Path(args.out)
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")
        result["out"] = str(out)
        result["out_mb"] = round(out.stat().st_size / 1048576, 2)
    if args.summary_out:
        sp = Path(args.summary_out)
        sp.parent.mkdir(parents=True, exist_ok=True)
        sp.write_text(json.dumps(result, ensure_ascii=False, indent=1), encoding="utf-8")
        result["summary_out"] = str(sp)
    if args.head_out:
        hp = Path(args.head_out)
        hp.parent.mkdir(parents=True, exist_ok=True)
        head = {
            "note": f"541 页全量解析的头部样例：前 {args.head_blocks} 个 blocks；完整 JSON 未入库",
            "doc_id": payload["doc_id"],
            "filename": payload["filename"],
            "page_count": payload["page_count"],
            "parser_version": payload["parser_version"],
            "pages": payload["pages"],
            "blocks": payload["blocks"][: args.head_blocks],
            "outline": payload["outline"][:60],
            "references": payload["references"][:20],
            "stats": payload["stats"],
        }
        hp.write_text(json.dumps(head, ensure_ascii=False, indent=1), encoding="utf-8")
        result["head_out"] = str(hp)
        result["head_out_mb"] = round(hp.stat().st_size / 1048576, 2)
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
