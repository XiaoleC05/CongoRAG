"""parser.py - PDF 学术解析核心（CongoRAG PDF sidecar v0）。

纯规则启发式（不调用大模型）：
  1. PyMuPDF get_text("dict") 提取 span/line/block 与 bbox，先合并成"视觉行"
  2. 页内 XY 分带 + 双栏检测恢复阅读顺序（宽块作为横向分隔带）
  3. 字号 / 加粗 / 编号正则 / 章节关键词识别标题树，跨页维护 section_path
  4. 数学符号密度 / 等宽字体启发式识别公式行并生成伪 LaTeX
  5. page.find_tables() 提取表格 → markdown
  6. References/Bibliography 节按 [n] / n. 行首标记切分参考文献
  7. 无文本层扫描页渲染 180dpi 后调用 RapidOCR（可选依赖，不可用自动降级）
  8. sha256(file) + 解析参数作为 key 落盘缓存

坐标系：blocks[].bbox 归一化为 0~1、左上原点（用 page.rect 尺寸归一化）。
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import threading
import time
from collections import Counter
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional

import pymupdf

PARSER_VERSION = "0.1.0"
PARSER_TAG = f"pdf-service-{PARSER_VERSION}"
BASE_DIR = Path(__file__).resolve().parent
DEFAULT_CACHE_DIR = BASE_DIR / "cache"

OCR_RENDER_DPI = int(os.environ.get("PDF_SERVICE_OCR_DPI", "180"))
AUTO_OCR_MAX_CHARS = 25      # ocr=auto: 纯文本层字符数低于此值才 OCR
ON_OCR_MAX_CHARS = 200       # ocr=on: 更激进地补 OCR
MIN_OCR_CHARS = 15
TABLE_MIN_FILL = 0.35
MAX_REF_RAW = 4000

# ---------------------------------------------------------------------------
# 异常
# ---------------------------------------------------------------------------


class PdfParseError(ValueError):
    """输入不是可解析的 PDF。"""


# ---------------------------------------------------------------------------
# 字符集 / 正则
# ---------------------------------------------------------------------------

MATH_CHARS = frozenset(
    "∑∫∏∮√∞≈≠≤≥±×÷∂∇∈∉∋⊂⊃⊆⊇∪∩∖∧∨¬∀∃⇒⇔→←↔↦∝≪≫≃≅≡⊕⊗⊙⊥∥∠⟨⟩‖∘∙"
    "αβγδεζηθικλμνξοπρστυφχψωΓΔΘΛΞΠΣΦΨΩ"
)
FOOTNOTE_MARKS = frozenset("∗†‡*")
TABLE_MAX_COLS = int(os.environ.get("PDF_SERVICE_TABLE_MAX_COLS", "20"))

LATEX_MAP = {
    "∑": r"\sum", "∫": r"\int", "∏": r"\prod", "∮": r"\oint", "√": r"\sqrt{}",
    "∞": r"\infty", "≈": r"\approx", "≠": r"\neq", "≤": r"\leq", "≥": r"\geq",
    "±": r"\pm", "×": r"\times", "÷": r"\div", "∂": r"\partial", "∇": r"\nabla",
    "∈": r"\in", "∉": r"\notin", "∋": r"\ni", "⊂": r"\subset", "⊃": r"\supset",
    "⊆": r"\subseteq", "⊇": r"\supseteq", "∪": r"\cup", "∩": r"\cap", "∧": r"\wedge",
    "∨": r"\vee", "¬": r"\neg", "∀": r"\forall", "∃": r"\exists", "⇒": r"\Rightarrow",
    "⇔": r"\Leftrightarrow", "→": r"\rightarrow", "←": r"\leftarrow",
    "↔": r"\leftrightarrow", "↦": r"\mapsto", "∝": r"\propto", "≪": r"\ll",
    "≫": r"\gg", "≃": r"\simeq", "≅": r"\cong", "≡": r"\equiv", "⊕": r"\oplus",
    "⊗": r"\otimes", "⊙": r"\odot", "⊥": r"\perp", "∥": r"\parallel", "∠": r"\angle",
    "⟨": r"\langle", "⟩": r"\rangle", "‖": r"\|", "†": r"\dagger", "‡": r"\ddagger",
    "∗": r"*", "∘": r"\circ", "∙": r"\cdot", "α": r"\alpha", "β": r"\beta",
    "γ": r"\gamma", "δ": r"\delta", "ε": r"\epsilon", "ζ": r"\zeta", "η": r"\eta",
    "θ": r"\theta", "ι": r"\iota", "κ": r"\kappa", "λ": r"\lambda", "μ": r"\mu",
    "ν": r"\nu", "ξ": r"\xi", "ο": "o", "π": r"\pi", "ρ": r"\rho", "σ": r"\sigma",
    "τ": r"\tau", "υ": r"\upsilon", "φ": r"\phi", "χ": r"\chi", "ψ": r"\psi",
    "ω": r"\omega", "Γ": r"\Gamma", "Δ": r"\Delta", "Θ": r"\Theta", "Λ": r"\Lambda",
    "Ξ": r"\Xi", "Π": r"\Pi", "Σ": r"\Sigma", "Φ": r"\Phi", "Ψ": r"\Psi",
    "Ω": r"\Omega", "–": "-", "—": "--", "−": "-", "′": r"^\prime",
}
SUP_UNICODE = {
    "⁰": "0", "¹": "1", "²": "2", "³": "3", "⁴": "4", "⁵": "5", "⁶": "6",
    "⁷": "7", "⁸": "8", "⁹": "9", "⁺": "+", "⁻": "-", "ⁿ": "n", "ᵢ": "i",
    "ⱼ": "j", "₀": "0", "₁": "1", "₂": "2", "₃": "3", "₄": "4", "₅": "5",
    "₆": "6", "₇": "7", "₈": "8", "₉": "9", "₊": "+", "₋": "-",
}

NUM_HEAD_RE = re.compile(r"^\s*(?:第\s*)?(\d{1,2}(?:\.\d{1,2}){0,2})\.?\s+([A-Z\u4e00-\u9fff][^\n]{0,150})$")
ROMAN_HEAD_RE = re.compile(r"^\s*([IVX]{1,4})\.\s+([A-Z][^\n]{0,150})$")
CAPTION_RE = re.compile(
    r"^\s*(?:Figure|Fig\.?|Table|Tab\.?|Algorithm|Listing|Eq(?:uation)?\.?|图|表)\s*"
    r"[A-Z]?\d+(?:[.\-–—:：]|\s|$)", re.I)
BULLET_RE = re.compile(
    r"^\s*(?:[•·▪◦‣∙●○■□➢→]|[-–—*]\s+|\d{1,2}[.)]\s+|\(\d{1,2}\)\s+|[a-z][.)]\s+|\d{1,2}\s+(?=\S))\S")
REF_MARK_RE = re.compile(r"^\s*(?:\[(\d{1,3})\](?!\d)|(\d{1,3})[.)](?!\d)\s|(\d{1,3})\s*$)")
CODE_HINT_RE = re.compile(r"^(?:def|class|import|from|return|if|for|while|print|var|let|const|function|#include)\b")
AFFIL_RE = re.compile(
    r"\b(?:University|Universit(?:y|é|ät)|Institute|Department|Laborator(?:y|ies)|"
    r"School of|College of|Faculty of|Inc\.?|Corporation|Research Center|Google|Microsoft|"
    r"Meta|Amazon|DeepMind|OpenAI|MIT|Stanford|Berkeley|CMU)\b")
CJK_RE = re.compile(r"[\u4e00-\u9fff\u3040-\u30ff]")

# 只保留"几乎一定是章节标题"的关键词；model/method/result 等通用词会把表头误判成标题
PAGE_NUM_RE = re.compile(r"^(?:\d{1,4}|[ivxlcdm]{1,7}|page\s+\d{1,4})$", re.I)
DATE_RE = re.compile(
    r"^(?:January|February|March|April|May|June|July|August|September|October|November|December|"
    r"Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec)\.?\s+\d{1,2},?\s+\d{4}$")
INITIALS_RE = re.compile(r"\b(?:[A-Z]\.){1,2}")
DOT_LEADER_RE = re.compile(r"(?:\.\s*){4,}")
TRAILING_NUM_RE = re.compile(r"\s+(?:\d{1,4}|[ivxlcdm]{1,7})$", re.I)
ROMAN_PREFIX_RE = re.compile(r"^[ivxlcdm]{1,7}\s+\S", re.I)
FOOTER_HINT_RE = re.compile(
    r"(?:Draft:|Creative Commons|All rights reserved|Copyright|ISBN|DOI:|Preprint|Under review|Confidential)",
    re.I)


def _key_text(text: str) -> str:
    """页眉/页脚去噪 key：去掉编号与尾随页码后的小写文本。"""
    t = re.sub(r"\s+", " ", text).strip().lower()
    t = re.sub(r"^[\d.\s]+", "", t)
    t = TRAILING_NUM_RE.sub("", t)
    return t.strip(" .:：-—–")[:80]


SECTION_KEYWORDS = {
    "abstract", "introduction", "related work", "related works",
    "conclusion", "conclusions", "references", "bibliography", "reference list",
    "acknowledgements", "acknowledgments", "acknowledgement", "appendix",
    "appendix a", "appendix b",
    "摘要", "引言", "绪论", "相关工作", "结论", "参考文献", "致谢", "附录",
}
REF_SECTION_NAMES = {"references", "bibliography", "reference list", "references and notes", "参考文献"}


def _norm_section(text: str) -> str:
    t = text.strip().lower()
    t = re.sub(r"^[\d.\s]+", "", t)
    t = t.strip(" .:：-—–\t")
    return re.sub(r"\s+", " ", t)


# ---------------------------------------------------------------------------
# 数据结构
# ---------------------------------------------------------------------------


@dataclass(slots=True)
class Span:
    text: str
    size: float
    flags: int
    x0: float
    y0: float
    x1: float
    y1: float

    @property
    def bold(self) -> bool:
        return bool(self.flags & 16)

    @property
    def mono(self) -> bool:
        return bool(self.flags & 8)


@dataclass(slots=True)
class Row:
    """视觉行：同一 y 上的多个 PyMuPDF line 合并后的结果。"""

    spans: list[Span]
    x0: float
    y0: float
    x1: float
    y1: float
    text: str
    max_size: float
    bold_ratio: float
    mono_ratio: float

    @property
    def width(self) -> float:
        return self.x1 - self.x0

    @property
    def height(self) -> float:
        return self.y1 - self.y0

    @property
    def center_x(self) -> float:
        return (self.x0 + self.x1) / 2.0

    @property
    def center_y(self) -> float:
        return (self.y0 + self.y1) / 2.0


@dataclass(slots=True)
class Unit:
    """解析单元 = 一个输出 block 的候选。"""

    page_no: int
    kind: str = "text"            # text | formula | table | ocr_text
    rows: list[Row] = field(default_factory=list)
    x0: float = 0.0
    y0: float = 0.0
    x1: float = 0.0
    y1: float = 0.0
    text: str = ""
    type: str = "paragraph"
    level: Optional[int] = None
    section_path: list[str] = field(default_factory=list)
    table_md: Optional[str] = None
    latex: Optional[str] = None
    ocr_median: float = 10.0

    def set_bbox(self, bbox: tuple[float, float, float, float]) -> None:
        self.x0, self.y0, self.x1, self.y1 = (float(v) for v in bbox)

    def set_bbox_from_rows(self) -> None:
        if not self.rows:
            return
        self.x0 = min(r.x0 for r in self.rows)
        self.y0 = min(r.y0 for r in self.rows)
        self.x1 = max(r.x1 for r in self.rows)
        self.y1 = max(r.y1 for r in self.rows)

    @property
    def width(self) -> float:
        return self.x1 - self.x0

    @property
    def height(self) -> float:
        return self.y1 - self.y0

    @property
    def center_x(self) -> float:
        return (self.x0 + self.x1) / 2.0

    @property
    def center_y(self) -> float:
        return (self.y0 + self.y1) / 2.0


# ---------------------------------------------------------------------------
# 文本工具
# ---------------------------------------------------------------------------


def _join_fragments(parts: list[str]) -> str:
    """把行片段拼成段落文本：英文补空格、去连字符换行、中文不补空格。"""
    out = ""
    for raw in parts:
        p = raw.strip()
        if not p:
            continue
        if not out:
            out = p
            continue
        if out.endswith("-") and len(out) > 1 and out[-2].isalpha() and p[:1].islower():
            out = out[:-1] + p
            continue
        if CJK_RE.search(out[-1]) or CJK_RE.search(p[0]):
            out += p
        else:
            out += " " + p
    return out


def _row_from_spans(spans: list[Span]) -> Row:
    spans.sort(key=lambda s: (s.x0, s.y0))
    x0 = min(s.x0 for s in spans)
    y0 = min(s.y0 for s in spans)
    x1 = max(s.x1 for s in spans)
    y1 = max(s.y1 for s in spans)
    max_size = max(s.size for s in spans) or 1.0
    total = bold = mono = 0
    parts: list[str] = []
    last_x1: Optional[float] = None
    for s in spans:
        total += len(s.text)
        if s.bold:
            bold += len(s.text)
        if s.mono:
            mono += len(s.text)
        txt = s.text
        if last_x1 is not None and not txt.startswith(" "):
            gap = s.x0 - last_x1
            if gap > 0.28 * max_size and not CJK_RE.search(parts[-1][-1:] or " "):
                parts.append(" ")
        parts.append(txt)
        last_x1 = s.x1
    text = re.sub(r"[ \t]+", " ", "".join(parts)).strip()
    return Row(
        spans=spans, x0=x0, y0=y0, x1=x1, y1=y1, text=text,
        max_size=max_size,
        bold_ratio=bold / total if total else 0.0,
        mono_ratio=mono / total if total else 0.0,
    )


def _rows_from_block(block: dict) -> list[Row]:
    """PyMuPDF block → 视觉行列表（同一 y 的 line 片段先合并）。"""
    raw_lines: list[list[Span]] = []
    for line in block.get("lines", []):
        spans: list[Span] = []
        for sp in line.get("spans", []):
            txt = sp.get("text", "")
            if not txt:
                continue
            x0, y0, x1, y1 = sp.get("bbox", (0, 0, 0, 0))
            spans.append(Span(
                text=txt, size=float(sp.get("size", 0.0) or 0.0),
                flags=int(sp.get("flags", 0) or 0),
                x0=float(x0), y0=float(y0), x1=float(x1), y1=float(y1),
            ))
        if spans:
            raw_lines.append(spans)
    if not raw_lines:
        return []
    merged: list[list[Span]] = []
    for spans in raw_lines:
        if not merged:
            merged.append(spans)
            continue
        cur = merged[-1]
        cur_y0 = min(s.y0 for s in cur)
        cur_y1 = max(s.y1 for s in cur)
        new_y0 = min(s.y0 for s in spans)
        new_y1 = max(s.y1 for s in spans)
        overlap = min(cur_y1, new_y1) - max(cur_y0, new_y0)
        min_h = max(min(cur_y1 - cur_y0, new_y1 - new_y0), 0.1)
        if overlap > 0.45 * min_h:
            cur.extend(spans)
        else:
            merged.append(spans)
    rows = [_row_from_spans(spans) for spans in merged]
    return [r for r in rows if r.text]


def _is_formula_row(row: Row, body_size: float, page_w: float) -> bool:
    """公式行启发式：数学符号密集 / 居中显示公式 / 上下标密集 / 等宽行。"""
    text = row.text.strip()
    if len(text) < 2 or len(text) > 260:
        return False
    if "@" in text or "http://" in text.lower() or "https://" in text.lower():
        return False
    if re.fullmatch(r"[\d\s.\-–—,;:()\[\]/]+", text):
        return False
    if any(c in FOOTNOTE_MARKS for c in text):
        return False
    chars = [c for c in text if not c.isspace()]
    if not chars:
        return False
    strong = sum(1 for c in chars if c in MATH_CHARS)
    density = strong / len(chars)
    has_eq = bool(re.search(r"[=≈≤≥<>]", text))
    has_subsup = any(s.size and body_size and s.size < 0.82 * body_size for s in row.spans)
    centered = row.x0 > 0.2 * page_w and row.x1 < 0.98 * page_w
    bold_only = row.bold_ratio >= 0.6 and not has_eq and strong == 0
    if bold_only:
        return False
    if strong >= 2 and density >= 0.06:
        return True
    if has_eq and strong >= 1 and density >= 0.03:
        return True
    words = text.split()
    if centered and len(text) <= 140 and len(words) <= 18 and (has_eq or has_subsup):
        if not text.endswith((".", "。", "!", "?")):
            return True
    if centered and has_subsup and len(text) <= 60:
        return True
    if row.mono_ratio >= 0.75 and len(text) < 140 and not CODE_HINT_RE.match(text):
        return True
    return False

def _span_latex(span: Span, base_size: float, line_center: float) -> str:
    text = "".join(SUP_UNICODE.get(c, c) for c in span.text)
    out = "".join(LATEX_MAP.get(c, c) for c in text)
    if not out.strip():
        return out
    size = span.size or base_size
    sc = (span.y0 + span.y1) / 2.0
    if size < base_size * 0.82:
        if sc < line_center - 0.06 * base_size:
            return "^{" + out.strip() + "}"
        if sc > line_center + 0.06 * base_size:
            return "_{" + out.strip() + "}"
    return out


def _row_latex(row: Row) -> str:
    base = row.max_size or 1.0
    center = row.center_y
    parts: list[str] = []
    last_x1: Optional[float] = None
    for sp in sorted(row.spans, key=lambda s: s.x0):
        px = sp.x1
        fragment = _span_latex(sp, base, center)
        if last_x1 is not None and sp.x0 - last_x1 > 0.28 * base:
            parts.append(" ")
        parts.append(fragment)
        last_x1 = px
    out = "".join(parts)
    out = re.sub(r"[ \t]+", " ", out).strip()
    out = out.replace("  ", " ")
    if len(out) > 2000:
        out = out[:2000] + "..."
    return out


def _unit_latex(unit: Unit) -> str:
    parts = [_row_latex(r) for r in unit.rows]
    parts = [p for p in parts if p]
    latex = r" \\ ".join(parts)
    return latex[:4000]


# ---------------------------------------------------------------------------
# 标题识别
# ---------------------------------------------------------------------------


def _clean_title(text: str) -> str:
    t = re.sub(r"\s+", " ", text).strip()
    t = t.strip(" .:：-—–\t")
    return t


def _heading_keyword(text: str) -> bool:
    key = _norm_section(text)
    if key in SECTION_KEYWORDS:
        return True
    if key.startswith(("appendix", "附录")):
        return True
    return False


def _classify_heading(unit: Unit, body_size: float) -> Optional[tuple[int, str]]:
    if unit.kind != "text" or len(unit.rows) != 1:
        return None
    row = unit.rows[0]
    text = unit.text.strip()
    if not text or len(text) > 180:
        return None
    words = text.split()
    if len(words) > 16 or "@" in text:
        return None
    if AFFIL_RE.search(text):
        return None
    if re.fullmatch(r"[\d\s.\-–—]+", text):
        return None
    if any(c in FOOTNOTE_MARKS for c in text):
        return None
    # 书眉行 "512 Bibliography" / "xii Contents"：剥掉前置页码后再判定
    header_pn = re.match(r"^(?:\d{2,4}|[ivxlcdm]{1,7})\s+(.+)$", text, re.I)
    if header_pn and _heading_keyword(header_pn.group(1)):
        text = header_pn.group(1).strip()
    num = NUM_HEAD_RE.match(text)
    roman = ROMAN_HEAD_RE.match(text)
    kw = _heading_keyword(text)
    if text.endswith((".", "!", "?", ";", ",")):
        if not (num or kw):
            return None
    if text.count(",") >= 2 and not (num or roman or kw):
        return None
    size = row.max_size or body_size
    ratio = size / body_size if body_size else 1.0
    bold = row.bold_ratio >= 0.55
    if not (num or roman or kw or bold or ratio >= 1.08):
        return None
    if not (num or roman or kw) and len(words) <= 1 and ratio < 1.18:
        return None  # 单个小字号加粗词（表头/图注）不作为标题
    if num:
        depth = len(num.group(1).split("."))
        level = min(3, depth)
    elif roman:
        level = 1
    elif kw:
        level = 1
    elif ratio >= 1.18:
        level = 1
    elif ratio >= 1.05 and bold:
        level = 2
    elif bold:
        level = 2 if len(words) <= 8 else 3
    else:
        level = 3
    title = _clean_title(text)
    if len(title) < 2:
        return None
    return level, title


def _classify_ocr_heading(unit: Unit, median_h: float) -> Optional[tuple[int, str]]:
    if unit.kind != "ocr_text" or len(unit.rows) != 1:
        return None
    raw = unit.text.strip()
    if not raw or len(raw) > 160:
        return None
    # OCR 常把 "7 Conclusion" 识别成 "7Conclusion"，仅为标题判定做一次拆分
    text = re.sub(r"(?<=\d)(?=[A-Z][a-z])", " ", raw)
    words = text.split()
    if len(words) > 16 or "@" in text:
        return None
    if re.fullmatch(r"[\d\s.\-–—]+", text):
        return None
    if text.endswith((".", "!", "?", ",", ";", ":")):
        return None
    if re.search(r"[<>\[\]=+/\\]", text):
        return None
    if DATE_RE.match(text):
        return None
    num = NUM_HEAD_RE.match(text)
    if num:
        return min(3, len(num.group(1).split("."))), _clean_title(text)
    if _heading_keyword(text):
        return 1, _clean_title(text)
    h = unit.rows[0].height
    # 仅"明显更大 + 首字母大写 + 无数字/符号"的短行才当作无编号标题（避免图内文字误报）
    if median_h > 0 and h >= 1.5 * median_h and len(words) <= 10:
        if text[:1].isupper() and not re.search(r"\d", text):
            level = 1 if h >= 2.0 * median_h else 2
            return level, _clean_title(text)
    return None

def _rows_to_markdown(rows: list[list[Any]]) -> str:
    clean: list[list[str]] = []
    for r in rows:
        cells: list[str] = []
        for c in r:
            if c is None:
                cells.append("")
                continue
            # 单元格内换行（合并单元格/多层表头）→ <br>，避免 markdown 结构塌陷
            val = " ".join(str(c).split("\n"))
            cells.append(re.sub(r"\s+", " ", val).strip())
        clean.append(cells)
    clean = [r for r in clean if any(r)]
    if not clean:
        return ""
    ncols = max(len(r) for r in clean)
    for r in clean:
        while len(r) < ncols:
            r.append("")
    header = clean[0]
    body = clean[1:]
    md = "| " + " | ".join(c.replace("|", r"\|") for c in header) + " |\n"
    md += "| " + " | ".join("---" for _ in header) + " |\n"
    for r in body:
        md += "| " + " | ".join(c.replace("|", r"\|") for c in r) + " |\n"
    return md


def _extract_table_units(page: pymupdf.Page, page_no: int) -> tuple[list[Unit], list[tuple[float, float, float, float]]]:
    units: list[Unit] = []
    boxes: list[tuple[float, float, float, float]] = []
    try:
        finder = page.find_tables()
        tables = getattr(finder, "tables", []) if finder is not None else []
    except Exception:
        return units, boxes
    page_w = page.rect.width
    for table in tables:
        try:
            rows = table.extract()
            bbox = tuple(float(v) for v in table.bbox)
        except Exception:
            continue
        if not rows or len(rows) < 2:
            continue
        ncols = max(len(r) for r in rows)
        if ncols < 2 or ncols > TABLE_MAX_COLS:
            continue
        total_cells = len(rows) * ncols
        filled = sum(1 for r in rows for c in r if c is not None and str(c).strip())
        if total_cells and filled / total_cells < TABLE_MIN_FILL:
            continue
        w = bbox[2] - bbox[0]
        h = bbox[3] - bbox[1]
        if w < 0.16 * page_w or h < 16:
            continue
        md = _rows_to_markdown(rows)
        if not md:
            continue
        text = "\n".join(
            " ".join(str(c).strip() for c in r if c is not None and str(c).strip())
            for r in rows if any(c is not None and str(c).strip() for c in r)
        )
        unit = Unit(page_no=page_no, kind="table", type="table", text=text, table_md=md)
        unit.set_bbox(bbox)
        units.append(unit)
        boxes.append(bbox)
    return units, boxes


def _unit_inside_box(unit: Unit, box: tuple[float, float, float, float]) -> bool:
    bx0, by0, bx1, by1 = box
    ix0 = max(unit.x0, bx0)
    iy0 = max(unit.y0, by0)
    ix1 = min(unit.x1, bx1)
    iy1 = min(unit.y1, by1)
    if ix1 <= ix0 or iy1 <= iy0:
        return False
    inter = (ix1 - ix0) * (iy1 - iy0)
    area = max((unit.x1 - unit.x0) * (unit.y1 - unit.y0), 1e-6)
    cx = unit.center_x
    cy = unit.center_y
    return inter / area > 0.6 or (bx0 < cx < bx1 and by0 < cy < by1)


# ---------------------------------------------------------------------------
# 阅读顺序
# ---------------------------------------------------------------------------


def _order_band(units: list[Unit], page_w: float) -> list[Unit]:
    """单条横向带内排序；能识别双栏时先左栏后右栏。"""
    if len(units) <= 1:
        return list(units)
    mass = sum(max(len(u.text), 1) for u in units)
    if len(units) < 4 or mass < 60:
        return sorted(units, key=lambda u: (u.y0, u.x0))
    mid = 0.5 * page_w
    tol = 0.035 * page_w
    left: list[Unit] = []
    right: list[Unit] = []
    full: list[Unit] = []
    for u in units:
        if u.x0 < mid - tol and u.x1 > mid + tol:
            full.append(u)
        elif u.x1 <= mid + tol and u.x0 < mid:
            left.append(u)
        elif u.x0 >= mid - tol and u.x1 > mid:
            right.append(u)
        else:
            full.append(u)
    if len(left) >= 2 and len(right) >= 2:
        lm = sum(len(u.text) for u in left)
        rm = sum(len(u.text) for u in right)
        if lm >= 0.2 * mass and rm >= 0.2 * mass and len(full) <= max(2, 0.35 * len(units)):
            ordered = sorted(left, key=lambda u: (u.y0, u.x0))
            ordered += sorted(right, key=lambda u: (u.y0, u.x0))
            ordered += sorted(full, key=lambda u: (u.y0, u.x0))
            return ordered
    return sorted(units, key=lambda u: (u.y0, u.x0))


def _reading_order(units: list[Unit], page_w: float, page_h: float) -> list[Unit]:
    """XY 分带 + 双栏阅读顺序。宽块（跨栏元素）作为横向分隔带。"""
    if len(units) <= 1:
        return list(units)
    wide = [u for u in units if u.width >= 0.62 * page_w]
    narrow = [u for u in units if u.width < 0.62 * page_w]
    if not wide:
        return _order_band(units, page_w)
    seps: list[tuple[float, float, list[Unit]]] = []
    for u in sorted(wide, key=lambda x: x.y0):
        if seps and u.y0 <= seps[-1][1] + 0.015 * page_h:
            sy0, sy1, group = seps[-1]
            seps[-1] = (sy0, max(sy1, u.y1), group + [u])
        else:
            seps.append((u.y0, u.y1, [u]))
    # 构建区域序列：band / sep 交替
    regions: list[list[Any]] = []
    prev_end = -1e9
    for sy0, sy1, group in seps:
        regions.append([prev_end, sy0, "band", []])
        regions.append([sy0, sy1, "sep", list(group)])
        prev_end = sy1
    regions.append([prev_end, 1e9, "band", []])
    for u in narrow:
        cy = u.center_y
        placed = False
        for region in regions:
            if region[0] <= cy < region[1]:
                region[3].append(u)
                placed = True
                break
        if not placed:
            regions[-1][3].append(u)
    ordered: list[Unit] = []
    for start, end, kind, arr in regions:
        if not arr:
            continue
        if kind == "band":
            ordered.extend(_order_band(arr, page_w))
        else:
            ordered.extend(sorted(arr, key=lambda u: (u.y0, u.x0)))
    return ordered


# ---------------------------------------------------------------------------
# 页面级上下文（页眉/页脚/TOC 页识别）
# ---------------------------------------------------------------------------


def _build_page_context(units: list[Unit], pages_meta: list[dict[str, Any]],
                        outline: Optional[list[dict[str, Any]]] = None) -> dict[str, Any]:
    dims = {p["page_no"]: (p["width"], p["height"]) for p in pages_meta}
    by_page: dict[int, list[Unit]] = {}
    for u in units:
        by_page.setdefault(u.page_no, []).append(u)

    toc_pages: set[int] = set()
    for pno, arr in by_page.items():
        w, _h = dims.get(pno, (612.0, 792.0))
        toc_like = 0
        for u in arr:
            t = u.text.strip()
            if not t:
                continue
            if DOT_LEADER_RE.search(t):
                toc_like += 2
            elif len(t.split()) <= 12 and TRAILING_NUM_RE.search(t) and u.width < 0.8 * w:
                toc_like += 1
        if toc_like >= 5:
            toc_pages.add(pno)

    top_counts: Counter[str] = Counter()
    bot_counts: Counter[str] = Counter()
    for pno, arr in by_page.items():
        _w, h = dims.get(pno, (612.0, 792.0))
        top_seen: set[str] = set()
        bot_seen: set[str] = set()
        for u in arr:
            text = u.text.strip()
            if not text or len(text) > 120:
                continue
            key = _key_text(text)
            if not key:
                continue
            if u.y0 < 0.18 * h:
                top_seen.add(key)
            elif u.y0 > 0.88 * h:
                bot_seen.add(key)
        top_counts.update(top_seen)
        bot_counts.update(bot_seen)

    # TOC 标题兜底表：用于恢复被页眉去噪误杀的章节标题（如 Bibliography）
    toc_by_key: dict[str, dict[str, Any]] = {}
    for entry in outline or []:
        title = str(entry.get("title", "")).strip()
        if not title:
            continue
        tk = _key_text(title)
        if tk and tk not in toc_by_key:
            toc_by_key[tk] = {"title": title, "level": int(entry.get("level") or 2)}

    return {
        "dims": dims,
        "toc_pages": toc_pages,
        "noise_top": {k for k, c in top_counts.items() if c >= 2},
        "noise_bot": {k for k, c in bot_counts.items() if c >= 2},
        "toc_by_key": toc_by_key,
    }


# ---------------------------------------------------------------------------
# OCR
# ---------------------------------------------------------------------------

_OCR_STATE: dict[str, Any] = {
    "checked": False, "available": False, "error": None,
    "engine": None, "lock": threading.Lock(), "det_limit": int(os.environ.get("PDF_SERVICE_OCR_DET_LIMIT", "0")),
}


def ocr_available() -> bool:
    if not _OCR_STATE["checked"]:
        try:
            import rapidocr_onnxruntime  # noqa: F401
            _OCR_STATE["available"] = True
        except Exception as exc:  # pragma: no cover - 环境相关
            _OCR_STATE["available"] = False
            _OCR_STATE["error"] = f"{type(exc).__name__}: {exc}"
        _OCR_STATE["checked"] = True
    return bool(_OCR_STATE["available"])


def _get_ocr_engine() -> Any:
    with _OCR_STATE["lock"]:
        if _OCR_STATE["engine"] is None:
            from rapidocr_onnxruntime import RapidOCR
            det_limit = int(_OCR_STATE.get("det_limit") or 1600)
            if det_limit > 0:
                # 实测 180dpi 整页时 RapidOCR 默认已按原分辨率检测，调高 limit 精度无收益，仅保留可配置能力
                _OCR_STATE["engine"] = RapidOCR(**{"Det.limit_side_len": det_limit})
            else:
                _OCR_STATE["engine"] = RapidOCR()
        return _OCR_STATE["engine"]


def _ocr_page_rows(page: pymupdf.Page, dpi: int) -> list[Row]:
    engine = _get_ocr_engine()
    import numpy as np  # 可选依赖：OCR 时才需要

    pix = page.get_pixmap(dpi=dpi, colorspace=pymupdf.csRGB)
    if pix.width == 0 or pix.height == 0:
        return []
    arr = np.frombuffer(pix.samples, dtype=np.uint8).reshape(pix.height, pix.width, pix.n)
    result, _ = engine(arr)
    if not result:
        return []
    scale = page.rect.width / float(pix.width)
    page_w = page.rect.width
    page_h = page.rect.height
    rows: list[Row] = []
    for item in result:
        try:
            box, text, score = item[0], item[1], item[2]
        except Exception:
            continue
        if text is None or not str(text).strip():
            continue
        try:
            if float(score) < 0.45:
                continue
        except Exception:
            pass
        xs = [float(p[0]) * scale for p in box]
        ys = [float(p[1]) * scale for p in box]
        x0, y0, x1, y1 = min(xs), min(ys), max(xs), max(ys)
        if x1 - x0 < 1 or y1 - y0 < 1:
            continue
        # 侧边栏（如 arXiv 竖排水印）与竖排文本
        if (x1 - x0) < 0.5 * page_w and (x1 < 0.1 * page_w or x0 > 0.9 * page_w):
            continue
        if (y1 - y0) > 0.2 * page_h and (x1 - x0) < 0.3 * page_w:
            continue
        h = y1 - y0
        span = Span(text=" ".join(str(text).split()), size=h, flags=0, x0=x0, y0=y0, x1=x1, y1=y1)
        row = _row_from_spans([span])
        rows.append(row)
    return rows


def _merge_ocr_units(ordered: list[Unit], page_w: float, page_h: float) -> list[Unit]:
    """把相邻 OCR 行合并为段落块。"""
    if not ordered:
        return []
    heights = sorted(u.height for u in ordered if u.height > 0)
    median_h = heights[len(heights) // 2] if heights else 10.0
    merged: list[Unit] = []
    for u in ordered:
        if merged:
            prev = merged[-1]
            gap = u.y0 - prev.y1
            same_col = abs(u.center_x - prev.center_x) < 0.25 * page_w
            left_align = abs(u.x0 - prev.x0) < 0.06 * page_w
            if (prev.kind == "ocr_text" and gap < 0.9 * median_h and same_col and left_align
                    and len(prev.text) < 3000):
                prev.rows.extend(u.rows)
                prev.set_bbox_from_rows()
                prev.text = _join_fragments([r.text for r in prev.rows])
                continue
        merged.append(u)
    return merged


# ---------------------------------------------------------------------------
# 参考文献
# ---------------------------------------------------------------------------


class ReferenceCollector:
    def __init__(self) -> None:
        self.refs: list[dict[str, Any]] = []
        self.current: Optional[dict[str, Any]] = None
        self.started = False
        self._last_no: Optional[int] = None

    def start(self) -> None:
        self.started = True

    def add(self, ref_no: Optional[int], text: str, page_no: int) -> None:
        raw = _join_fragments(text.split("\n")) if "\n" in text else text.strip()
        raw = re.sub(r"\s+", " ", raw).strip()
        if not raw:
            return
        if ref_no is not None:
            if ref_no == self._last_no:
                return
            entry = {"ref_no": int(ref_no), "raw": raw[:MAX_REF_RAW], "page": page_no}
            self.refs.append(entry)
            self.current = entry
            self._last_no = int(ref_no)
        elif self.current is not None:
            merged = (self.current["raw"] + " " + raw).strip()
            self.current["raw"] = merged[:MAX_REF_RAW]
        self._trim()

    def _trim(self) -> None:
        if len(self.refs) > 2000:
            self.refs = self.refs[:2000]


def _split_ref_rows(rows: list[Row]) -> list[tuple[list[Row], Optional[int]]]:
    segments: list[tuple[list[Row], Optional[int]]] = []
    cur: list[Row] = []
    cur_no: Optional[int] = None
    for row in rows:
        m = REF_MARK_RE.match(row.text.strip())
        if m:
            if cur:
                segments.append((cur, cur_no))
            cur = [row]
            cur_no = int(next(g for g in m.groups() if g))
        else:
            cur.append(row)
    if cur:
        segments.append((cur, cur_no))
    return segments


def _refs_look_valid(refs: list[dict[str, Any]]) -> bool:
    """编号参考文献应有近似连续递增的 ref_no；作者-年份制会被误切，需回滚。"""
    if not refs:
        return False
    if refs[0]["ref_no"] > 5:
        return False
    if len(refs) >= 3:
        inc = sum(1 for a, b in zip(refs, refs[1:]) if b["ref_no"] > a["ref_no"])
        if inc / (len(refs) - 1) < 0.8:
            return False
    return True


# ---------------------------------------------------------------------------
# 缓存
# ---------------------------------------------------------------------------


class JsonCache:
    def __init__(self, root: Optional[Path] = None) -> None:
        env_dir = os.environ.get("PDF_SERVICE_CACHE_DIR")
        self.root = Path(root or env_dir or DEFAULT_CACHE_DIR)
        self.lock = threading.Lock()

    def key(self, sha: str, ocr: str, max_pages: Optional[int], tables: bool) -> str:
        mp = "all" if not max_pages else str(max_pages)
        return f"{sha[:2]}/{sha}-v{PARSER_VERSION}-{ocr}-{mp}-t{int(tables)}.json"

    def get(self, key: str) -> Optional[dict[str, Any]]:
        path = self.root / key
        try:
            with path.open("r", encoding="utf-8") as fh:
                payload = json.load(fh)
        except (OSError, ValueError):
            return None
        if not isinstance(payload, dict) or payload.get("parser_version") not in (
                PARSER_VERSION, PARSER_TAG):
            return None
        if "blocks" not in payload or "pages" not in payload:
            return None
        return payload

    def put(self, key: str, payload: dict[str, Any]) -> None:
        path = self.root / key
        try:
            with self.lock:
                path.parent.mkdir(parents=True, exist_ok=True)
                tmp = path.with_suffix(path.suffix + f".tmp{os.getpid()}")
                with tmp.open("w", encoding="utf-8") as fh:
                    json.dump(payload, fh, ensure_ascii=False, separators=(",", ":"))
                os.replace(tmp, path)
        except OSError:
            pass


_DEFAULT_CACHE: Optional[JsonCache] = None
_CACHE_LOCK = threading.Lock()


def default_cache() -> JsonCache:
    global _DEFAULT_CACHE
    with _CACHE_LOCK:
        if _DEFAULT_CACHE is None:
            _DEFAULT_CACHE = JsonCache()
        return _DEFAULT_CACHE


# ---------------------------------------------------------------------------
# 页面解析
# ---------------------------------------------------------------------------


def _estimate_body_size(doc: pymupdf.Document, limit: int) -> float:
    counter: Counter[float] = Counter()
    sample_n = min(limit, doc.page_count)
    if sample_n <= 0:
        return 10.0
    step = max(1, sample_n // 20)
    for i in range(0, sample_n, step):
        try:
            page = doc[i]
            data = page.get_text("dict")
        except Exception:
            continue
        for block in data.get("blocks", []):
            if block.get("type") != 0:
                continue
            for line in block.get("lines", []):
                for sp in line.get("spans", []):
                    txt = sp.get("text", "").strip()
                    if not txt:
                        continue
                    size = round(float(sp.get("size", 0.0) or 0.0), 1)
                    if size <= 0:
                        continue
                    counter[size] += len(txt)
    if not counter:
        return 10.0
    return float(counter.most_common(1)[0][0])


def _is_sidebar_block(bbox: tuple[float, float, float, float], page_w: float, page_h: float) -> bool:
    x0, y0, x1, y1 = bbox
    w = x1 - x0
    h = y1 - y0
    if w <= 0 or h <= 0:
        return True
    if w < 0.07 * page_w and (x1 < 0.1 * page_w or x0 > 0.9 * page_w):
        return True
    if h > 0.55 * page_h and w < 0.12 * page_w:
        return True
    return False


def _page_units(page: pymupdf.Page, page_no: int, body_size: float,
                detect_tables: bool) -> tuple[list[Unit], list[tuple[float, float, float, float]]]:
    page_w = page.rect.width
    page_h = page.rect.height
    table_units: list[Unit] = []
    table_boxes: list[tuple[float, float, float, float]] = []
    if detect_tables:
        table_units, table_boxes = _extract_table_units(page, page_no)

    units: list[Unit] = []
    try:
        data = page.get_text("dict")
    except Exception:
        data = {"blocks": []}
    for block in data.get("blocks", []):
        if block.get("type") != 0:
            continue
        bbox = tuple(float(v) for v in block.get("bbox", (0, 0, 0, 0)))
        if _is_sidebar_block(bbox, page_w, page_h):
            continue
        rows = _rows_from_block(block)
        if not rows:
            continue
        cur: list[Row] = []

        def flush() -> None:
            nonlocal cur
            if not cur:
                return
            u = Unit(page_no=page_no, kind="text", rows=list(cur))
            u.set_bbox_from_rows()
            u.text = _join_fragments([r.text for r in cur])
            units.append(u)
            cur = []

        for row in rows:
            if _is_formula_row(row, body_size, page_w):
                flush()
                if units and units[-1].kind == "formula" and units[-1].page_no == page_no:
                    gap = row.y0 - units[-1].y1
                    if gap < 0.9 * max(row.height, 1.0):
                        prev = units[-1]
                        prev.rows.append(row)
                        prev.set_bbox_from_rows()
                        prev.text = _join_fragments([r.text for r in prev.rows])
                        continue
                fu = Unit(page_no=page_no, kind="formula", rows=[row])
                fu.set_bbox_from_rows()
                fu.text = row.text.strip()
                units.append(fu)
                continue
            if cur:
                prev = cur[-1]
                delta_y = row.y0 - prev.y0
                if delta_y > 1.55 * max(body_size, 1.0) or delta_y < -0.5 * max(row.height, 1.0):
                    flush()
            cur.append(row)
        flush()

    # 表格区域内的文本块剔除（避免与 table_md 重复）
    if table_boxes and units:
        filtered: list[Unit] = []
        for u in units:
            if any(_unit_inside_box(u, tb) for tb in table_boxes):
                continue
            filtered.append(u)
        units = filtered
    units.extend(table_units)
    return units, table_boxes


# ---------------------------------------------------------------------------
# 主解析
# ---------------------------------------------------------------------------


class PdfParser:
    def __init__(self, detect_tables: bool = True, ocr_dpi: int = OCR_RENDER_DPI) -> None:
        self.detect_tables = detect_tables
        self.ocr_dpi = ocr_dpi

    # -- 内部 ---------------------------------------------------------------
    def _analyze_pages(self, doc: pymupdf.Document, max_pages: Optional[int],
                       ocr_mode: str) -> tuple[list[Unit], dict[str, Any]]:
        total = doc.page_count
        limit = total if not max_pages else min(total, int(max_pages))
        body_size = _estimate_body_size(doc, limit)
        self.body_size = body_size
        ocr_usable = ocr_mode != "off" and ocr_available()

        pages_meta: list[dict[str, Any]] = []
        all_units: list[Unit] = []
        ocr_pages = 0
        warnings: list[str] = []
        if ocr_mode != "off" and not ocr_usable:
            warnings.append(
                "OCR requested but rapidocr_onnxruntime unavailable; "
                "scanned pages will produce no blocks (install requirements-ocr.txt)")

        for idx in range(limit):
            page = doc[idx]
            page_no = idx + 1
            page_w = page.rect.width
            page_h = page.rect.height
            pages_meta.append({
                "page_no": page_no,
                "width": round(page_w, 3),
                "height": round(page_h, 3),
                "rotation": int(page.rotation),
            })
            try:
                text_len = len(page.get_text("text").strip())
            except Exception:
                text_len = 0
            threshold = ON_OCR_MAX_CHARS if ocr_mode == "on" else AUTO_OCR_MAX_CHARS
            use_ocr = False
            if ocr_usable and text_len < threshold:
                try:
                    has_images = bool(page.get_images(full=False))
                except Exception:
                    has_images = False
                if has_images:
                    use_ocr = True
                elif text_len < MIN_OCR_CHARS and ocr_mode == "on":
                    use_ocr = bool(page.get_drawings())

            if use_ocr:
                try:
                    rows = _ocr_page_rows(page, self.ocr_dpi)
                    ocr_pages += 1
                    if rows:
                        units = [Unit(page_no=page_no, kind="ocr_text", rows=[r]) for r in rows]
                        for u in units:
                            u.set_bbox_from_rows()
                            u.text = u.rows[0].text
                        ordered = _reading_order(units, page_w, page_h)
                        units = _merge_ocr_units(ordered, page_w, page_h)
                        heights = sorted(u.height for u in units if u.height > 0)
                        median_h = heights[len(heights) // 2] if heights else body_size
                        for u in units:
                            u.type = "paragraph"
                            u.ocr_median = median_h
                        all_units.extend(units)
                        continue
                except Exception as exc:  # OCR 失败降级为文本层
                    warnings.append(f"OCR failed on page {page_no}: {type(exc).__name__}: {exc}")
                    ocr_pages += 1
            units, _ = _page_units(page, page_no, body_size, self.detect_tables)
            ordered = _reading_order(units, page_w, page_h)
            all_units.extend(ordered)

        meta = {
            "body_size": body_size,
            "pages_meta": pages_meta,
            "ocr_pages": ocr_pages,
            "warnings": warnings,
            "limit": limit,
            "page_count_total": total,
        }
        return all_units, meta

    # -- 对外 ---------------------------------------------------------------
    def parse(self, data: bytes, filename: str = "upload.pdf",
              ocr: str = "auto", max_pages: Optional[int] = None) -> dict[str, Any]:
        started = time.perf_counter()
        ocr = (ocr or "auto").lower().strip()
        if ocr not in {"auto", "on", "off"}:
            raise PdfParseError(f"invalid ocr mode: {ocr!r} (expected auto|on|off)")
        if max_pages is not None:
            try:
                max_pages = int(max_pages)
            except (TypeError, ValueError):
                raise PdfParseError("max_pages must be an integer")
            if max_pages <= 0:
                max_pages = None
        try:
            doc = pymupdf.open(stream=data, filetype="pdf")
        except Exception as exc:
            raise PdfParseError(f"cannot open PDF: {exc}") from exc
        if doc.needs_pass:
            doc.close()
            raise PdfParseError("PDF is encrypted (password required)")
        try:
            if doc.page_count == 0:
                raise PdfParseError("PDF has no pages")
            try:
                outline = [
                    {"title": str(t).strip(), "level": int(lv), "page": int(pg)}
                    for lv, t, pg in doc.get_toc() if str(t).strip()
                ]
            except Exception:
                outline = []
            units, meta = self._analyze_pages(doc, max_pages, ocr)
        finally:
            try:
                doc.close()
            except Exception:
                pass

        ctx = _build_page_context(units, meta["pages_meta"], outline)
        blocks_out, references, section_aliases = self._assemble(
            units, meta["pages_meta"], ctx)
        elapsed_ms = int((time.perf_counter() - started) * 1000)
        pages_meta = meta["pages_meta"]
        payload = {
            "doc_id": hashlib.sha256(data).hexdigest(),
            "filename": filename,
            "page_count": int(meta["page_count_total"]),
            "parser_version": PARSER_TAG,
            "pages": pages_meta,
            "blocks": blocks_out,
            "outline": outline,
            "references": references,
            "stats": {
                "pages_parsed": len(pages_meta),
                "blocks": len(blocks_out),
                "ocr_pages": int(meta["ocr_pages"]),
                "elapsed_ms": elapsed_ms,
            },
        }
        if meta["warnings"]:
            payload["warnings"] = meta["warnings"]
        return payload

    # -- 组装 blocks / references -------------------------------------------
    def _assemble(self, units: list[Unit], pages_meta: list[dict[str, Any]],
                  ctx: dict[str, Any]) -> tuple[list[dict[str, Any]], list[dict[str, Any]], set[str]]:
        page_dims = {p["page_no"]: (p["width"], p["height"]) for p in pages_meta}
        blocks: list[dict[str, Any]] = []
        refs = ReferenceCollector()
        stack: list[tuple[int, str]] = []
        in_refs = False
        seen_headings: set[str] = set()
        emitted_norm: set[str] = set()
        page_block_seq: dict[int, int] = {}

        def current_path() -> list[str]:
            return [title for _, title in stack]

        def add_block(unit: Unit, btype: str, text: str, level: Optional[int] = None) -> None:
            page_no = int(unit.page_no)
            seq = page_block_seq.get(page_no, 0) + 1
            page_block_seq[page_no] = seq
            w, h = page_dims.get(unit.page_no, (1.0, 1.0))
            bx0 = min(max(unit.x0 / w, 0.0), 1.0) if w else 0.0
            by0 = min(max(unit.y0 / h, 0.0), 1.0) if h else 0.0
            bx1 = min(max(unit.x1 / w, 0.0), 1.0) if w else 0.0
            by1 = min(max(unit.y1 / h, 0.0), 1.0) if h else 0.0
            item: dict[str, Any] = {
                "id": f"p{page_no}-b{seq:02d}",
                "page": int(unit.page_no),
                "bbox": [round(bx0, 4), round(by0, 4), round(bx1, 4), round(by1, 4)],
                "type": btype,
                "text": text,
            }
            if level is not None:
                item["level"] = int(level)
            item["section_path"] = list(unit.section_path)
            if unit.table_md:
                item["table_md"] = unit.table_md
            if unit.latex:
                item["latex"] = unit.latex
            blocks.append(item)

        def update_stack(level: int, title: str) -> list[str]:
            while stack and stack[-1][0] >= level:
                stack.pop()
            stack.append((level, title))
            return current_path()

        for unit in units:
            page_w, page_h = page_dims.get(unit.page_no, (1.0, 1.0))
            text = unit.text.strip()
            if not text:
                continue
            near_top = unit.y0 < 0.18 * page_h
            near_bot = unit.y0 > 0.88 * page_h
            key = _key_text(text)
            body_size = getattr(self, "body_size", 10.0) or 10.0
            big_text = bool(unit.rows) and any(r.max_size > 1.3 * body_size for r in unit.rows)
            heading = None
            if unit.kind in {"text", "ocr_text"}:
                if unit.kind == "ocr_text":
                    heading = _classify_ocr_heading(unit, unit.ocr_median)
                else:
                    heading = _classify_heading(unit, body_size)
            # TOC 兜底：本应出现但被跨页页眉去噪误杀的章节标题（如 Bibliography）
            rescued = False
            if (near_top and key and key in ctx["noise_top"] and not big_text
                    and key not in emitted_norm):
                toc_entry = ctx["toc_by_key"].get(key)
                if toc_entry:
                    heading = (int(toc_entry["level"] or 2), _clean_title(toc_entry["title"]))
                    rescued = True
            if near_top and key and key in ctx["noise_top"] and not big_text and not rescued:
                continue  # 重复出现的页眉（书眉/章节名/页码组合），大字号/TOC 兜底标题保留
            if near_bot and key and key in ctx["noise_bot"] and not big_text:
                continue  # 重复出现的页脚
            if (near_top or near_bot) and PAGE_NUM_RE.match(text):
                continue  # 页码
            if near_bot and len(text) < 160 and FOOTER_HINT_RE.search(text):
                continue  # 版权/草稿/DOI 等页脚
            if unit.kind == "table":
                unit.section_path = current_path()
                add_block(unit, "table", text, None)
                continue

            if heading is not None:
                if (unit.page_no in ctx["toc_pages"]
                        or (near_top and not rescued
                            and (TRAILING_NUM_RE.search(text)
                                 or ROMAN_PREFIX_RE.match(text)
                                 or "contents" in text.lower()))
                        or DATE_RE.match(text)
                        or (INITIALS_RE.search(text) and len(text.split()) <= 8)):
                    heading = None
            if heading is not None:
                level, title = heading
                key = (title.lower(), level)
                if unit.y0 < 0.09 * page_h and title.lower() in seen_headings:
                    continue  # 页眉重复标题，跳过
                if title.lower() in seen_headings and len(title) <= 24 and unit.y0 < 0.12 * page_h:
                    continue
                seen_headings.add(title.lower())
                emitted_norm.add(_key_text(title))
                unit.section_path = update_stack(level, title)
                add_block(unit, "heading", title, level)
                norm = _norm_section(title)
                if norm in REF_SECTION_NAMES:
                    in_refs = True
                    refs.start()
                elif in_refs:
                    in_refs = False
                continue

            if in_refs and unit.kind in {"text", "ocr_text"} and unit.rows:
                segments = _split_ref_rows(unit.rows)
                for seg_rows, ref_no in segments:
                    if not seg_rows:
                        continue
                    seg = Unit(page_no=unit.page_no, kind=unit.kind, rows=seg_rows)
                    seg.set_bbox_from_rows()
                    seg.text = _join_fragments([r.text for r in seg_rows])
                    if not seg.text.strip():
                        continue
                    if ref_no is None and refs.current is None:
                        seg.section_path = current_path()
                        add_block(seg, "paragraph", seg.text, None)
                        continue
                    refs.add(ref_no, seg.text, seg.page_no)
                    seg.section_path = current_path()
                    add_block(seg, "reference", seg.text, None)
                continue

            unit.section_path = current_path()
            if unit.kind == "formula":
                unit.latex = _unit_latex(unit)
                add_block(unit, "formula", unit.text, None)
                continue
            text = unit.text.strip()
            if not text:
                continue
            if CAPTION_RE.match(text):
                add_block(unit, "caption", text, None)
                continue
            if unit.rows and BULLET_RE.match(unit.rows[0].text.strip() or ""):
                add_block(unit, "list", text, None)
                continue
            add_block(unit, "paragraph", text, None)

        if not _refs_look_valid(refs.refs):
            # 作者-年份制参考文献（如 UDL）用 [n]/n. 规则会误切，回滚为普通段落
            for block in blocks:
                if block["type"] == "reference":
                    block["type"] = "paragraph"
            refs = ReferenceCollector()

        return blocks, refs.refs, seen_headings


# ---------------------------------------------------------------------------
# 对外入口
# ---------------------------------------------------------------------------


def _sanitize_filename(filename: Optional[str]) -> str:
    if not filename:
        return "upload.pdf"
    name = Path(filename.replace("\\", "/")).name.strip()
    if not name:
        return "upload.pdf"
    if len(name) > 240:
        name = name[:240]
    return name


def parse_pdf_ex(data: bytes, filename: str = "upload.pdf", ocr: str = "auto",
                 max_pages: Optional[int] = None, use_cache: bool = True,
                 cache: Optional[JsonCache] = None,
                 detect_tables: Optional[bool] = None) -> tuple[dict[str, Any], dict[str, Any]]:
    """解析 PDF，返回 (payload, meta)。meta 供 HTTP 层写响应头/日志。"""
    if not data:
        raise PdfParseError("empty file")
    started = time.perf_counter()
    sha = hashlib.sha256(data).hexdigest()
    ocr_mode = (ocr or "auto").lower().strip()
    if ocr_mode not in {"auto", "on", "off"}:
        raise PdfParseError(f"invalid ocr mode: {ocr_mode!r} (expected auto|on|off)")
    if max_pages is not None:
        try:
            max_pages = int(max_pages)
        except (TypeError, ValueError):
            raise PdfParseError("max_pages must be an integer")
        if max_pages <= 0:
            max_pages = None
    tables = detect_tables if detect_tables is not None else detect_tables_enabled()
    cache_obj = cache or default_cache()
    key = cache_obj.key(sha, ocr_mode, max_pages, tables)
    if use_cache:
        cached = cache_obj.get(key)
        if cached is not None:
            cached["doc_id"] = sha
            cached["filename"] = _sanitize_filename(filename)
            meta = {
                "cache_hit": True,
                "ocr": "off" if ocr_mode == "off" else ("available" if ocr_available() else "unavailable"),
                "elapsed_ms": int((time.perf_counter() - started) * 1000),
            }
            return cached, meta
    parser = PdfParser(detect_tables=tables)
    payload = parser.parse(data, _sanitize_filename(filename), ocr_mode, max_pages)
    meta = {
        "cache_hit": False,
        "ocr": "off" if ocr_mode == "off" else ("available" if ocr_available() else "unavailable"),
        "elapsed_ms": int((time.perf_counter() - started) * 1000),
    }
    if use_cache:
        cache_obj.put(key, payload)
    return payload, meta


def parse_pdf(data: bytes, filename: str = "upload.pdf", ocr: str = "auto",
              max_pages: Optional[int] = None, use_cache: bool = True) -> dict[str, Any]:
    payload, _meta = parse_pdf_ex(data, filename, ocr, max_pages, use_cache)
    return payload


def detect_tables_enabled() -> bool:
    return os.environ.get("PDF_SERVICE_DETECT_TABLES", "1").lower() not in {"0", "false", "off", "no"}


def parse_pdf_path(path: str | Path, ocr: str = "auto", max_pages: Optional[int] = None,
                   use_cache: bool = True) -> dict[str, Any]:
    p = Path(path)
    return parse_pdf(p.read_bytes(), p.name, ocr, max_pages, use_cache)
