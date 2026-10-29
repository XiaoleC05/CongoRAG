# pdf-service v0 — 科研学术 PDF 解析 sidecar

CongoRAG 主系统用 Go 编写，缺少 PDF 解析能力。本目录是一个**独立 Python sidecar**：
输入 PDF（HTTP multipart），输出结构化 JSON；Go 侧仅需一次 HTTP 调用，不引入 Python 依赖、不碰 Go 仓库。

- **纯规则启发式**（字号/加粗/编号/符号密度/线框），不调用大模型、无 GPU、无外部服务
- 文本层：PyMuPDF `get_text("dict")`；扫描页：RapidOCR（**可选依赖，缺失自动降级**）
- 缓存：`sha256(file) + 解析参数` 落盘 JSON，重复请求毫秒级返回
- 大文档：逐页流式解析（内存有界），`max_pages` 可先试跑、缓存不重复解析

> 已与 `_dev/CONTRACT_pdf-service.md` 对齐：默认监听 `127.0.0.1:8000`、块 id 为 `p{page}-b{seq}`、
> `max_pages=0` 表示全部、响应 `parser_version=pdf-service-0.1.0`。

---

## 1. 3 分钟跑通

环境：Windows + Python 3.12（本机使用 bundled python，依赖已装好）。

```bat
:: 1) 安装依赖（只需一次）
"C:\Users\71408\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe" -m pip install -r requirements.txt
:: 可选：扫描件 OCR（不装则自动降级，只解析文本层）
"C:\Users\71408\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe" -m pip install -r requirements-ocr.txt

:: 2) 启动（双击 run.bat 亦可）
run.bat
```

等价手动启动：

```bat
cd /d C:\Users\71408\Desktop\传智杯\AI创新应用\_dev\pdf-service
python -m uvicorn server:app --host 127.0.0.1 --port 8000
```

```bat
:: 3) 健康检查
curl.exe -s http://127.0.0.1:8000/healthz
:: {"status":"ok","version":"0.1.0","ocr_available":true}

:: 4) 解析一个 PDF
curl.exe -s -o out.json -X POST -F "file=@C:\Users\71408\Desktop\传智杯\AI创新应用\测试数据\pdf\1706.03762v1.pdf" "http://127.0.0.1:8000/parse?ocr=auto"

:: 5) 大文档先试跑前 20 页
curl.exe -s -o udl20.json -X POST -F "file=@C:\Users\71408\Desktop\传智杯\AI创新应用\测试数据\pdf\UnderstandingDeepLearning_02_09_26_C.pdf" "http://127.0.0.1:8000/parse?ocr=auto&max_pages=20"
```

本机依赖已预装（fastapi 0.162 / uvicorn 0.54 / pymupdf 1.28 / python-multipart 0.0.32 / rapidocr_onnxruntime 1.4.4）。
其它机器用普通 `python -m venv .venv` + `pip install -r requirements.txt` 即可；`run.bat` 会优先使用 `.venv`。

---

## 2. API 契约

### 2.1 `GET /healthz`

```json
{"status": "ok", "version": "0.1.0", "ocr_available": true}
```

`ocr_available` 只探测 import 是否成功，不加载模型（不阻塞）。健康检查语义：进程活着、解析核心可 import。

### 2.2 `POST /parse`

| 项 | 说明 |
| --- | --- |
| Content-Type | `multipart/form-data`，字段名 `file` |
| Query `ocr` | `auto`（默认）/`on`/`off`，见 `2.4 |
| Query `max_pages` | 只解析前 N 页（**0=全部**，1~5000），大文档试跑用；缺省解析全部 |
| 响应头 | `X-PDF-Cache: hit|miss`、`X-PDF-OCR: available|unavailable|off`、`X-PDF-Elapsed-Ms`、`X-PDF-Wall-Ms`、`X-PDF-Parser-Version` |
| 错误码 | `400` 空文件 / `413` 超 200MB / `422` 非 PDF 或加密 PDF / `500` 内部错误 |

响应 JSON（`blocks[].bbox` 为归一化 0~1、**左上原点**，按 `page.rect` 宽高归一化）：

```json
{
  "doc_id": "0a9bff1be3575cf9...（sha256(文件) 全 64 位）",
  "filename": "1706.03762v1.pdf",
  "page_count": 15,
  "parser_version": "pdf-service-0.1.0",
  "pages": [{"page_no": 1, "width": 612.0, "height": 792.0, "rotation": 0}],
  "blocks": [
    {
      "id": "p1-b01",
      "page": 1,
      "bbox": [0.3456, 0.1261, 0.6534, 0.1478],
      "type": "heading",
      "text": "Attention Is All You Need",
      "level": 1,
      "section_path": ["Attention Is All You Need"]
    },
    {
      "id": "p1-b03",
      "page": 1,
      "bbox": [0.1765, 0.4091, 0.7647, 0.6604],
      "type": "paragraph",
      "text": "The dominant sequence transduction models ...",
      "section_path": ["Attention Is All You Need", "Abstract"]
    },
    {
      "id": "p9-b01",
      "page": 9,
      "bbox": [0.1765, 0.164, 0.8312, 0.4691],
      "type": "table",
      "text": "Model | train ...",
      "table_md": "| Model | ... |\n| --- | ... |\n...",
      "section_path": ["6 Results"]
    },
    {
      "id": "p4-b02",
      "page": 4,
      "bbox": [0.3595, 0.6729, 0.6626, 0.7014],
      "type": "formula",
      "text": "MultiHead(Q, K, V ) = Concat(head1, ..., headh)",
      "latex": "MultiHead(Q, K, V ) = Concat(head_{1}, ..., head_{h})",
      "section_path": ["3 Model Architecture", "3.2 Attention", "3.2.2 Multi-Head Attention"]
    }
  ],
  "outline": [{"title": "1 Introduction", "level": 1, "page": 1}],
  "references": [{"ref_no": 1, "raw": "[1] Jimmy Lei Ba, ... 2016.", "page": 10}],
  "stats": {"pages_parsed": 15, "blocks": 434, "ocr_pages": 0, "elapsed_ms": 1000}
}
```

字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `doc_id` | str | `sha256(原始文件字节)` 全 64 位十六进制 |
| `page_count` | int | **文档总页数**（不代表本次解析页数） |
| `pages[]` | list | 本次解析到的页：`page_no` 1-based、宽高（pt）、`rotation` |
| `blocks[].type` | enum | `heading / paragraph / formula / table / list / reference / caption / other` |
| `blocks[].level` | int? | 仅 heading：1~3 级 |
| `blocks[].section_path` | list[str] | 标题栈路径；heading 块包含自身，正文为祖先标题 |
| `blocks[].table_md` | str? | 仅 table：markdown 表格 |
| `blocks[].latex` | str? | 仅 formula：伪 LaTeX（占位级，见 `5） |
| `outline` | list | 源 PDF 书签目录（`doc.get_toc()` 原样转换，可能为空） |
| `references[]` | list | 参考文献：`ref_no`（int）、`raw`（合并去连字符后的文本）、`page` |
| `stats.ocr_pages` | int | 实际调用 OCR 的页数（含 OCR 无结果的空白扫描页） |
| `warnings` | list? | 可选；OCR 失败等降级信息，仅非空时出现 |

`other` 类型预留给后续版本，v0 目前只产出其余 7 类。

### 2.3 返回内容约定

- 只返回结构化块，不返回图片、不做 Markdown 全文拼接；全文可由 `blocks[].text` 按序拼接
- `blocks` 已按**恢复的阅读顺序**排列（跨页按页码升序），可据此直接顺序消费
- 归一化 bbox 适合前端高亮；如需 pt 坐标乘 `pages[].width/height`
- `page_count` 为全文总页数，`stats.pages_parsed` 为本次解析页数（`max_pages` 时两者不同）

### 2.4 OCR 策略与降级

| `ocr` | 行为 |
| --- | --- |
| `off` | 从不对扫描页 OCR；无文本层页面返回 0 块 |
| `auto`（默认） | 文本层字符数 < 25 且页面含位图 → 渲染 180dpi 调 RapidOCR；否则走文本层 |
| `on` | 更激进：文本层字符数 < 200 且含位图 → OCR（适合"文本层有少量水印/无正文"的扫描件） |

降级链（从高到低）：
1. RapidOCR 可用 + 扫描页 → OCR 块（`type=paragraph`，无字号/加粗信息）；
2. RapidOCR 不可用（未安装/模型缺失）→ 扫描页返回 0 块，响应头 `X-PDF-OCR: unavailable`，
   `warnings` 记录原因，`ocr_pages=0`；进程**不会**因缺 OCR 依赖而启动失败；
3. OCR 单页异常 → 该页回退文本层解析，`warnings` 记录页码与异常类型。

`ocr_available=false` 时 Go 侧可据此决定是否提示用户"扫描件未解析"，或改用其它通道。

---

## 3. 缓存

- 目录：`cache/`（可用 `PDF_SERVICE_CACHE_DIR` 覆盖），首次请求后写入，进程重启复用
- Key：`sha256[:2]/sha256-v<parser_version>-<ocr>-<max_pages|all>-t<0|1>.json`
  （`t` = 是否启用表格检测）
- 命中条件：sha256 相同 + `ocr` 相同 + `max_pages` 相同 + 解析器版本相同
- `parser_version` 变化自动失效旧缓存（避免升级逻辑后返回旧结构）
- 原子写入（临时文件 + `os.replace`），并发同 key 可能重复写但不会读到半个文件
- 不做 TTL/容量上限；缓存 JSON 可直接作为离线样例消费

> Go 侧建议以 `doc_id + 参数` 作为本地缓存 key，避免重复上传大文件。

---

## 4. 解析算法（实现位于 `parser.py`）

1. **文本层**：`page.get_text("dict")` 取 span（字号/字体/flags/bbox）；同一 y 的 line 片段合并为"视觉行"
2. **阅读顺序**：页内 XY 分带 —— 宽度 ≥ 0.62 页宽的块视为跨栏分隔带；带内按左右半边 + 字符量判定双栏，
   左栏读完再读右栏；无跨栏块则整页双栏判定。恢复顺序对双栏论文/单栏书稿均适用
3. **标题树**：单视觉行 + 短文本 + （编号正则 `1. / 1.1 / 1.1.1`、章节关键词、字号 ≥ 1.18×正文字号、
   加粗短行）判 heading；按编号深度/字号比例分级 1~3；维护跨页标题栈生成 `section_path`
4. **公式**：数学符号密度（∑∫√∈…）≥6%、含 `=` 且符号密度≥3%、居中且带上下标、
   或等宽字体行 → formula；用符号映射 + 上下标位置生成伪 LaTeX
5. **表格**：`page.find_tables()`（PyMuPDF 1.24+，线框检测）→ `table_md`；
   过滤 >20 列 / 填充率 <35% / 过小的误检；表格区域内的文本块不重复输出
6. **参考文献**：命中 References/Bibliography 标题后，按行首 `[n]` / `n.` 切分，续行合并，
   输出 `ref_no/raw/page`，同区块标 `type=reference`
7. **扫描页 OCR**：整页渲染（150~180dpi，本机默认 180）→ RapidOCR → 行块 → 同栏相邻行合并段落；
   标题按行高/编号/关键词弱识别
8. **噪声过滤**：页码、页眉页脚（跨页重复文本）、TOC 页（点引导线/尾随页码）、侧边栏竖排水印、版权行

---

## 5. 限制与已知问题（v0）

- **启发式，非模型**：双栏/单栏效果最好；三栏以上、竖排、复杂杂志版式仅基础支持
- **公式为占位级伪 LaTeX**：只做 unicode → LaTeX 符号映射与上下标，不保证可编译；公式行切分可能碎成多块
- **表格**：仅线框表格可靠；无边框/跨页表格会漏检；图内线条可能误检（已按列数/填充率/尺寸过滤）
- **参考文献**：只支持 `[n]` / `n.` 行首编号；作者-年份制会主动回滚为空列表（不产出错误编号），无编号引用列表不识别；不解析交叉引用
- **扫描件**：OCR 无粗体/字号信息，标题树较弱；本机 180dpi 单页稳态约 2.3s，HTTP 端到端 15 页扫描件 85~88s（约 5.7s/页，含模型首次加载/序列化）
- **页眉页脚**：基于跨页重复统计，仅出现一次的页眉可能残留为段落块
- **旋转页**：bbox 按 `page.rect` 归一化，未做 rotate 坐标变换（官方 4 个测试文件 rotation 均为 0）
- **不返回**：图片内容、公式截图、字体名、词级 bbox、分栏拓扑
- 无鉴权/限流；单进程 uvicorn；上传上限 200MB（`PDF_SERVICE_MAX_UPLOAD_MB`）
- 内存：541 页全量实测见 `TEST-REPORT.md`；逐页解析，峰值主要来自结果 JSON 与 PyMuPDF 缓存

---

## 6. 目录结构

```
_dev/pdf-service/
├─ server.py              FastAPI 服务（/healthz、/parse）
├─ parser.py              解析核心（可独立 import，不依赖 FastAPI）
├─ requirements.txt       核心依赖
├─ requirements-ocr.txt   可选 OCR 依赖
├─ run.bat                Windows 启动脚本
├─ cache/                 解析结果缓存（运行时生成）
├─ samples/               官方测试数据实测输出 JSON + summary.json
├─ scripts/
│  ├─ e2e_test.py         对运行中的服务做 4 个用例端到端实测
│  └─ bench.py            直接调 parser 的耗时/内存基准（541 页全量用）
├─ TEST-REPORT.md         实测报告（耗时/块数/OCR 准确率/限制）
└─ README.md
```

## 7. 测试

```bat
:: 服务需先在 8000 运行；产物写入 samples/
python scripts\e2e_test.py --base-url http://127.0.0.1:8000

:: 541 页全量基准（不写缓存、输出全量 JSON）
python scripts\bench.py --pdf "C:\Users\71408\Desktop\传智杯\AI创新应用\测试数据\pdf\UnderstandingDeepLearning_02_09_26_C.pdf" --no-cache --out "%TEMP%\udl_full.json"
```

实测结果与样例见 `samples/` 与 `TEST-REPORT.md`。

---

## 8. Go 侧接入建议

```go
req, _ := http.NewRequest("POST", baseURL+"/parse?ocr=auto", body) // body: multipart
req.Header.Set("Content-Type", w.FormDataContentType())
client := &http.Client{Timeout: 10 * time.Minute} // 大文档 + OCR 需要长超时
resp, err := client.Do(req)
// 响应头 X-PDF-Cache: hit 表示未真正解析；X-PDF-OCR: unavailable 表示扫描件降级
```

- 长文档建议先 `max_pages=20` 出首屏，再后台全量（爬虫/索引任务）
- `doc_id` 即文件 sha256，可直接作为向量库/文档表主键
- `blocks[].section_path` 可直接作为 chunk 的 section 元数据，`level` 用于标题树展示
- 服务无鉴权，仅监听 `127.0.0.1`；跨机调用请自行加内网网关/鉴权

---

## 9. 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `PDF_SERVICE_HOST` / `PDF_SERVICE_PORT` | `127.0.0.1` / `8000` | 监听地址/端口（run.bat 与 `python server.py` 生效） |
| `PDF_SERVICE_CACHE_DIR` | `<服务目录>/cache` | 缓存目录 |
| `PDF_SERVICE_OCR_DPI` | `180` | OCR 渲染 dpi（150~200 建议） |
| `PDF_SERVICE_OCR_DET_LIMIT` | `0` | RapidOCR 检测边限制；0=用库默认（180dpi 下已按原分辨率检测，实测调高无精度收益） |
| `PDF_SERVICE_DETECT_TABLES` | `1` | 是否启用 `find_tables`（0 关闭可提速） |
| `PDF_SERVICE_TABLE_MAX_COLS` | `20` | 表格最大列数，超过视为误检丢弃 |
| `PDF_SERVICE_MAX_UPLOAD_MB` | `200` | 上传大小上限 |
| `PDF_SERVICE_MAX_PAGES_LIMIT` | `5000` | `max_pages` 上限 |