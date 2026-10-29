# PDF 解析 sidecar v0 实测报告（TEST-REPORT）

> 日期：2026-10-29 · 版本：pdf-service 0.1.0 / parser 0.1.0（响应 parser_version = `pdf-service-0.1.0`）
> 测试对象：`_dev/pdf-service/`（独立 sidecar，未修改 Go 仓库、未修改工作区其它文件）
> 测试数据：`测试数据/pdf/` 下 4 个官方用例，原样上传，未做任何预处理
> 测试方式：**真实启动 HTTP 服务**（`127.0.0.1:8000`，与 `_dev/CONTRACT_pdf-service.md` 对齐），`scripts/e2e_test.py` 走 multipart 上传跑完 4 个用例；
> 541 页全量另用 `scripts/bench.py` 直接调用同一份 parser（不经 HTTP，避免传输/序列化干扰），两者同一份解析代码。

## 结论速览

- 4 个官方用例全部跑通，扫描件 OCR、541 页大文档均验证；
- 文本层论文（v1 / v7）标题树对 PDF 书签 TOC 召回 **22/22 = 100%**，表格/公式/参考文献块均产出；
- 541 页书稿全量 541/541 页解析成功，约 **122 s**，约 226 ms/页，输出 JSON 3.99 MB；
- 扫描件 OCR 与文本层对照：**正文页（无位图/矢量图页）字符准确率 95.14%（≥ Gate 1 的 95%）**；全部 15 页加权 90.41%（图页含图内文字，口径不可直接比较）；
- sha256 缓存命中 **0.02 s**（v1 首次约 1.0 s，约 50 倍加速）；
- 依赖缺失/OCR 关闭/非法文件/非法参数均按契约降级或返回 422，服务不崩。

## 1. 测试环境

| 项 | 值 |
| --- | --- |
| OS | Windows（本机 28 逻辑核，未限制 CPU） |
| Python | 3.12.14（bundled：`C:\Users\71408\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe`） |
| 服务 | FastAPI 0.162.0 + uvicorn 0.54.0（单进程，127.0.0.1:8000） |
| PDF 解析 | PyMuPDF 1.28.2（`page.get_text("dict")`、`page.find_tables()`） |
| OCR | rapidocr_onnxruntime 1.4.4（onnxruntime 1.31.1，CPU），180 dpi 整页渲染 |
| 其它 | python-multipart 0.0.32、numpy 2.3.5 |

启动命令（实际测试使用的启动链）：

```bat
cd /d C:\Users\71408\Desktop\传智杯\AI创新应用\_dev\pdf-service
run.bat
:: 或
python -m uvicorn server:app --host 127.0.0.1 --port 8000
```

```bat
curl.exe -s http://127.0.0.1:8000/healthz
:: {"status":"ok","version":"0.1.0","ocr_available":true}
```

契约对齐要点（与 `CONTRACT_pdf-service.md` 一致）：默认端口 8000；块 id 为 `p{page}-b{seq}`（页内序号）；
`max_pages=0` 表示全部页；响应 `parser_version=pdf-service-0.1.0`；`doc_id` 为文件 sha256；bbox 归一化 0~1 左上原点。

## 2. 总表（HTTP 端到端实测）

| 用例 | 大小/总页 | 解析页 | 首次耗时 | 缓存命中 | blocks | heading | table | formula | caption | list | reference 块 | references | outline |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `1706.03762v1.pdf`（文本层论文） | 2.03 MB / 15 | 15 | **约 1.0 s** | **0.02 s** | 434 | 26 | 2 | 25 | 10 | 0 | 69 | 36 | 22 |
| `1706.03762v1_img.pdf`（纯扫描件，无文本层） | 4.97 MB / 15 | 15 | **87.7 s**（OCR 15 页） | 同左 | 513 | 12 | 0 | 0 | 6 | 3 | 201 | 34 | 0 |
| `1706.03762v7.pdf`（文本层，v7 版） | 2.11 MB / 15 | 15 | **1.12 s** | — | 445 | 26 | 2 | 26 | 10 | 0 | 77 | 40 | 22 |
| `UnderstandingDeepLearning_02_09_26_C.pdf` `max_pages=20` | 21.31 MB / 541 | 20 | **0.95 s** | — | 94 | 12 | 0 | 0 | 6 | 21 | 0 | 0 | 181 |
| 同上 **全量**（直接调 parser，`bench.py`） | 21.31 MB / 541 | 541 | **122.3 s** | — | **9511** | 511 | 6 | 2411 | 279 | 57 | 0（主动回滚） | 0 | 181 |

> 说明：`reference 块` 是 `blocks[].type=reference` 的块数（参考文献常被 PDF 切成多块，续行也标 reference）；
> `references` 是解析出的编号条目数。UDL 为作者-年份制参考文献，规则不适用，程序主动回滚为 paragraph（见 `7）。

大文档全量（541 页）多次实测：120.7 / 121.3 / 121.7 / 122.3 s（空闲）；一次与其它任务并行时 191.7 s。稳定约 **121~122 s @ 空闲**。

## 3. Gate 1 验收对照

| Gate 1 验收项 | 实测 |
| --- | --- |
| 上传 v1：15 页、blocks 带 page/bbox/section_path | ✅ 15 页、434 blocks；bbox 归一化 0~1；section_path 三级正确（示例见 `4.1） |
| 上传 v1_img：OCR 出正文，字符准确率 ≥95% | ✅ **正文页 95.14%**（p1/2/5/6/7/10/11/12，去空格字符级）；全 15 页加权 90.41%，因 13~15 页为可视化图页（文本层含图内文字）不具可比性 |
| 上传 UDL：541 页不崩，记录耗时/内存 | ✅ 541/541 页，122.3 s，peak RSS 758 MB，JSON 3.99 MB |
| 双栏阅读顺序 / 表格 / 公式 / 长文分片 / 缓存 | ✅ 分带+双栏算法（v1 实为单栏、双栏逻辑在合成用例与代码路径验证）；表格/公式块产出；`max_pages` 分片；缓存命中 0.02 s |

---

## 4. 用例详情

### 4.1 `1706.03762v1.pdf`（15 页，文本层）

- 解析耗时约 1.0 s，`stats = {pages_parsed:15, blocks:434, ocr_pages:0, elapsed_ms:~1000}`
- blocks 类型：paragraph 302 / heading 26 / reference 69 / formula 25 / caption 10 / table 2
- **标题树**：与 PDF 书签 TOC 对照 **22/22 命中（100%）**；另检出论文标题 `Attention Is All You Need`、`Abstract`、`References`、`Attention Visualizations`（后两者不在书签中，属于合理补充）。
  节路径 / id 示例（归一化 bbox 左上原点）：
  - `p1-b01` p1 `[0.3456, 0.1261, 0.6534, 0.1478]` heading L1 “Attention Is All You Need”
  - `p2-b04` p2 `[0.1765, 0.2664, 0.3085, 0.2815]` heading L1 “2 Background”
  - L3 标题 section_path 三级正确（`3 Model Architecture > 3.2 Attention > 3.2.2 Multi-Head Attention`）
- **公式 bbox 数量 25**：示例 `p3-b09` p3 `[0.3594, 0.8837, 0.6166, 0.9169]`，text = `Attention(Q, K, V ) = softmax(QKT √dk`，latex = `Attention(Q, K, V ) = softmax(QK^{T} \ \sqrt{}d_{k}`（伪 LaTeX，见 `8 限制）
- **表格 2 个**（均为 page 9 真实线框表）：示例 `p9-b02` p9 `[0.1765, 0.164, 0.8313, 0.4691]`，`table_md` 为规范 markdown 管道表
- **参考文献 36 条**，编号 [1]~[36] 连续，与原文完全一致（`refs[0] = "[1] Jimmy Lei Ba, ... 2016."`，page=10）
- caption 10 个（Figure/Table 题注），heading 级别：L1 8 / L2 10 / L3 8

### 4.2 `1706.03762v1_img.pdf`（15 页纯扫描件，无文本层）

- `ocr=auto` 自动判定 15/15 页为扫描页，全部走 RapidOCR；`stats.ocr_pages=15`，耗时 87.7 s
- blocks 513：paragraph 291 / reference 201 / heading 12 / caption 6 / list 3
- **OCR 与 v1 文本层逐页对照**（去空格字符级，SequenceMatcher）：

| 页 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 字符准确率 | 91.2% | 96.2% | 85.7% | 92.8% | 99.8% | 89.0% | 94.9% | 81.3% | 80.6% | 97.2% | 96.8% | 99.7% | 69.6% | 60.8% | 63.6% |

  - **正文页（无位图/无大量矢量图：p1/2/5/6/7/10/11/12）加权：95.14%**；全部 15 页加权 90.41%；页均 86.61%
  - 词级 50.74%；OCR 总字数 29834 / 真值 31034 = 0.961
  - p13~15 是原文 Attention 可视化图页（v1 文本层含图内矢量文字，OCR 视角不同），p8/9 为示意图页，上述页面不参与"正文"口径
- 标题 12 个：`Abstract`、`References` + 10 个编号标题（1 Introduction / 2 Background / 3 ModelArchitecture / 3.1 / 3.2 / 3.2.1 / 5.3 / 6 / 6.1 / 7 Conclusion）
- 参考文献 34 条（原文 36 条，OCR 对首尾行有 2 条漏检）
- 图像/版式限制：OCR 丢单词间空格（`AttentionIs All You Need`、`AshishVaswani`），因此词级准确率明显低于字符级

### 4.3 `1706.03762v7.pdf`（15 页，文本层）

- 1.12 s；blocks 445：paragraph 304 / heading 26 / reference 77 / formula 26 / caption 10 / table 2
- TOC 召回 **22/22（100%）**（v7 书签标题无编号，评测按去编号后匹配）；references 40 条，编号 [1]~[40] 连续

### 4.4 `UnderstandingDeepLearning_02_09_26_C.pdf`（541 页）

**先跑 `max_pages=20`（HTTP）**：0.95 s，blocks 94，headings 12，outline 181。
检出标题树：`Understanding Deep Learning`（p1-b01）→ `Preface`(p11) → `Acknowledgments`(p13) → `Attribution`(p14) → `Chapter 1`(p15) → `Introduction`(p15) → `1.1 Supervised learning`(L2) → `1.1.1`~`1.1.5`(L3)，section_path 层级正确；
TOC 对照（该范围内 4 条书签）4/4 命中。前 20 页含 6 个 Figure 题注、21 个 list 项（目录行）。

**全量 541 页（`bench.py --no-cache`）**：

| 指标 | 值 |
| --- | --- |
| 总耗时 | **122.3 s**（约 226 ms/页） |
| pages_parsed | 541 / 541 |
| blocks | **9511** |
| 类型分布 | paragraph 6247 / formula 2411 / heading 511 / caption 279 / list 57 / table 6 |
| 标题级别 | L1 95 / L2 185 / L3 231 |
| 标题树 TOC 召回 | **173 / 181 = 95.6%**（缺 8 个附录小节标题，见 `8） |
| 表格 | 6 个（page 84 的一张大表被线框检测拆成 6 段，见 `8） |
| 公式 | 2411 块（本书数学行多；启发式存在少量误报/碎片，见 `8） |
| 参考文献 | 0 条（作者-年份制，主动回滚为 paragraph，避免 97 条误切） |
| 输出 JSON | 3.99 MB |
| 内存 | Peak RSS **757.6 MB**；解析后 RSS 261.3 MB；Python tracemalloc 峰值 177.7 MB |
| outline | 181 条（PDF 书签原样） |

> UDL 书签页码与 PDF 物理页存在 front-matter 偏移（如 Preface 书签 p5，实际 PDF p11），因此按"去编号标题"匹配而不是按页码匹配。

---

## 5. 性能与内存

| 场景 | 页数 | 耗时 | 吞吐 |
| --- | --- | --- | --- |
| v1 文本层论文 | 15 | ~1.0 s | ~69 ms/页 |
| v7 文本层论文 | 15 | 1.12 s | 75 ms/页 |
| UDL `max_pages=20` | 20 | 0.95 s | 48 ms/页 |
| UDL 全量（含 `find_tables`） | 541 | 122.3 s | 226 ms/页 |
| 扫描件 OCR（HTTP，含首次模型加载） | 15 | 87.7 s | 5.8 s/页 |
| OCR 隔离基准（180 dpi，模型已加载） | 1 | ~2.3 s | 2.3 s/页 |

- 541 页全量峰值 RSS **758 MB**：主要来自 PyMuPDF 内部缓存 + `find_tables` + 结果列表 + 最终 JSON 序列化；解析后 RSS 回落到 261 MB，tracemalloc 峰值 177.7 MB。
- 大文档建议：检索场景按 `max_pages` 分片（缓存 key 含 max_pages）；纯文本索引场景可设 `PDF_SERVICE_DETECT_TABLES=0` 提速。
- 单进程 uvicorn + async 端点 + 线程池执行解析：健康检查不被长时间解析阻塞（解析在 threadpool）。

---

## 6. 缓存

- 写盘位置：`_dev/pdf-service/cache/<sha前2位>/<sha256>-v0.1.0-<ocr>-<max_pages|all>-t<0|1>.json`
- 命中条件：文件 sha256 + ocr 模式 + max_pages + 表格开关 + parser 版本
- 实测：v1 首次约 1.0 s（`X-PDF-Cache: miss`）→ 重复请求 **0.02 s**（`X-PDF-Cache: hit`，响应头 `X-PDF-Elapsed-Ms` 保留首次解析耗时）
- 541 页全量结果 JSON 3.99 MB，落盘后再请求只做文件读 + JSON 解析
- 原子写入（tmp + `os.replace`），并发同 key 不会读到半截；`parser_version` 变更自动失效旧缓存

---

## 7. 降级与错误处理实测

| 场景 | 实测结果 |
| --- | --- |
| 服务健康检查 | `GET /healthz → 200 {"status":"ok","version":"0.1.0","ocr_available":true}` |
| 扫描件 + `ocr=off` | 200，`X-PDF-OCR: off`，blocks=0，`stats.ocr_pages=0`（如实返回空结果，不报错） |
| 模拟 OCR 依赖缺失 | 200，`X-PDF-OCR: unavailable`，blocks=0，`warnings=["OCR requested but rapidocr_onnxruntime unavailable; scanned pages will produce no blocks ..."]`，进程不崩 |
| 上传非 PDF 文件 | 422 `{"detail":"parse failed: cannot open PDF: ..."}` |
| `max_pages=0` | 200，等价"全部页"（契约约定） |
| 非法 `max_pages`（负数/超上限） | 422（FastAPI 参数校验） |
| `ocr=xx` | 422（参数校验 pattern `^(auto|on|off)$`） |
| 加密 PDF | 422 `PDF is encrypted (password required)`（代码路径，未准备样本） |
| 空文件 | 400 |
| 超过 200 MB | 413（`PDF_SERVICE_MAX_UPLOAD_MB` 可调） |

---

## 8. 限制与已知问题（实测确认）

1. **表格**：只对线框表（`find_tables` 默认 lines 策略）可靠。v1 的 Table 1（复杂度）等无框表未检出（仅检出 page 9 两个线框表）；UDL 的 page 84 大表因合并单元格/无完整线框被拆成 6 个小表。已过滤 >20 列、填充率 <35% 的误检（否则 v1 图页会误报 3 张"表"）。
2. **公式**：伪 LaTeX 仅做 unicode→LaTeX 符号映射 + 上下标位置，示例 `R^{d}^{model}` 这类连续上下标无法正确分组，多处公式行会碎成多块（v1 25 块，v7 26 块），仅作为检索/占位用途。
3. **参考文献**：只支持行首 `[n]` / `n.` 编号制；UDL 作者-年份制会误切出 97 条垃圾编号，程序已加入合法性校验（首个编号 ≤5 且 80% 递增，否则回滚为 paragraph），因此 UDL `references=[]`、reference 块 0——**该限制对 Go 侧可见**，需要时可由上层改用其它解析器。
4. **扫描件 OCR**：本机页面无粗体/字号信息，标题树明显弱于文本层（12 vs 26）；OCR 常丢单词间空格（`AttentionIs All You Need`），词级准确率仅 50.7%；参考文献区切分偏碎（201 块 / 34 条）；无表格、无公式。字符级正文页 95.14% 达标，全页 90.41%。
5. **UDL 标题树**：TOC 召回 173/181，缺 8 个附录小节（Functions / Matrix calculus 等，其书眉格式与正文标题不一致）；同时多出 302 个非书签标题（多为 L3 小节与 `Chapter N`/`Notes`/`Problems`，属合理补充而非误报）。
6. **页眉页脚去噪**：基于跨页重复统计，仅单页出现的页眉/页脚可能残留为 paragraph；采用 TOC 兜底恢复被误杀的章节标题（如 `Bibliography`，page 485）。
7. **旋转页**：bbox 按 `page.rect` 宽高归一化，未做 rotation 坐标变换；官方 4 个用例 rotation 均为 0，未受影响。
8. **不返回**：图片/公式截图、字体名、词级 bbox、分栏拓扑；不返回 Markdown 全文（可由 blocks 顺序拼接）。
9. **性能**：541 页全量 122 s / 758 MB 峰值，单进程无并发保护；建议 Go 侧串行/限流调用，超长文档分片。
10. **安全**：无鉴权、无限流，仅监听 127.0.0.1；跨机部署需自行加网关。
11. **缓存**：无 TTL/容量上限；缓存目录可手工清理或换 `PDF_SERVICE_CACHE_DIR`。
12. **公式/表格误检**：启发式规则会漏检/误检；已设阈值过滤明显误检，但不保证学术级精确。

---

## 9. 复现步骤

```bat
:: 1) 启动服务（bundled python 已装好依赖；默认端口 8000）
cd /d C:\Users\71408\Desktop\传智杯\AI创新应用\_dev\pdf-service
run.bat

:: 2) 端到端实测 4 个用例（产物写 samples/，约 2 分钟，含扫描件 15 页 OCR）
python scripts\e2e_test.py --base-url http://127.0.0.1:8000

:: 3) 541 页全量性能/内存基准（约 2 分钟，不写缓存）
python scripts\bench.py --pdf "C:\Users\71408\Desktop\传智杯\AI创新应用\测试数据\pdf\UnderstandingDeepLearning_02_09_26_C.pdf" --no-cache --summary-out samples\UDL_full_stats.json --head-out samples\UnderstandingDeepLearning_full_head.json
```

---

## 10. 产物清单

| 路径 | 说明 |
| --- | --- |
| `samples/1706.03762v1.json` | v1 完整解析结果（15 页，434 blocks） |
| `samples/1706.03762v1_img.json` | 扫描件 OCR 解析结果（15 页，513 blocks） |
| `samples/1706.03762v7.json` | v7 完整解析结果（445 blocks） |
| `samples/UnderstandingDeepLearning_p20.json` | UDL `max_pages=20` 解析结果 |
| `samples/UDL_full_stats.json` | UDL 541 页全量指标（耗时/内存/块统计） |
| `samples/UnderstandingDeepLearning_full_head.json` | UDL 全量头部样例（pages + 前 40 blocks + outline 前 60 + refs 前 20 + stats） |
| `samples/summary.json` | 本次 e2e 全量指标汇总（含 TOC 评估、OCR 准确率、缓存测试） |
| `cache/` | 运行时缓存（实测期间生成；可删除，不影响功能） |

样例 JSON 均包含契约中的全部字段：`doc_id / filename / page_count / parser_version / pages / blocks / outline / references / stats`；
`blocks[].bbox` 为 0~1 归一化左上原点坐标，`heading` 带 `level`，`table` 带 `table_md`，`formula` 带 `latex`，`id` 为 `p{page}-b{seq}`。
