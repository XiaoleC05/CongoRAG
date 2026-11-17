
# 省赛提交材料（四件套）

> 传智杯「AI 大模型创新应用挑战赛」· 参赛编号 **1885438** · 陈晓乐 · 西华大学
> **提交版：2026-11-18，tag `v6.7.2`**。本目录是随仓库分发的提交材料副本，评委可直接查看。

## 一、四件套

| # | 材料 | 文件 / 链接 |
|---|---|---|
| 1 | 代码仓库 | https://github.com/XiaoleC05/CongoRAG（main，Public，tag `v6.7.2`） |
| 2 | 演示视频（5:56，1920×1080，中文字幕） | [CongoRAG科研助手_演示视频_陈晓乐_传智杯2026.mp4](CongoRAG科研助手_演示视频_陈晓乐_传智杯2026.mp4)（字幕 [.srt](CongoRAG科研助手_演示视频_陈晓乐_传智杯2026.srt)） |
| 3 | 技术文档（PDF，27 页 ≤30） | [CongoRAG科研助手_技术文档_陈晓乐.pdf](CongoRAG科研助手_技术文档_陈晓乐.pdf) |
| 4 | 在线演示 | <https://congorag.cloud>（公开首页；token 入口见下） |

## 二、支撑材料

- [作品介绍文档_陈晓乐.pdf](作品介绍文档_陈晓乐.pdf)：按官网表单 7 项要求编写（项目详情/亮点/AI 落地/关键技术/截图/发表获奖/优化说明）
- [评测报告.md](评测报告.md)：检索 hit@1 71.4% / hit@3 100% / MRR 0.833；引用页码 3/3；grounding 2/3（2026-10-29 本机评测）
- [六模块验收报告.md](六模块验收报告.md)：12 项 API 回归（12/12，2026-10-29）+ 2026-11-18 在线复核说明
- [评审访问说明.md](评审访问说明.md)：一页版评审路径（四件套入口 + 5 分钟演示顺序）
- [评审自检清单_陈晓乐_已填.xlsx](评审自检清单_陈晓乐_已填.xlsx)：45 项自评（含 2026-11-18 复核页）
- [在线演示信息.txt](在线演示信息.txt)：演示地址、账号、数据与备案
- `assets/`：技术文档配图（9 张，含架构图；界面图取自 v6.1.0 演示库）

## 三、在线演示

- 首页：<https://congorag.cloud/>（页脚展示 ICP + 公安网备）
- token 免密入口：<https://congorag.cloud/?token=0d728319beef03b7da2a812591bf74ae>
- 账号入口：<https://congorag.cloud/>（reviewer / CongoRAG2026）
- 备案：ICP **鲁ICP备2026038838号-2**；公安联网备案 **鲁公网安备37028202001377号**
- 2026-11-18 复核：首页 200、token 入口 200、5 个演示知识库、17 条 Prompt 审计记录；示例会话可直接打开。

## 四、推荐演示路径

1. 知识库 → PDF 学术解析验证 → 文档状态/检索调试（三路分数、页码）；
2. 对话 → 示例：引用溯源 → 悬停角标 →「在原文中查看」→ 阅读器第 7 页 bbox；
3. 对话 → 示例：无证据拒答 → 固定拒答 + 引用校验徽标；
4. 阅读器：目录/双语对照/划词翻译/参考文献；
5. 知识库 → 引用图谱演示 → 生成图谱（PageRank）/自动综述；
6. 写作 Copilot + CSV 可视化；
7. Agent → arXiv 文献助手：回放已完成运行（tool_call/tool_result）；
8. Prompt 审计（17 条：版本/SHA-256/代码位置）+ Ctrl+K 命令面板。

## 五、复现

- 仓库根 [README](../../README.md)：快速开始、环境配置、Docker 部署
- [deployments/production/README.md](../../deployments/production/README.md)：生产部署与 PDF sidecar
- [docs/modules.md](../modules.md)：六模块与架构；[docs/prompt-audit.md](../prompt-audit.md)：Prompt 审计

## 六、版本说明

技术文档主体写于 2026-11-13，界面截图取自 v6.1.0 演示库；v6.2–v6.7 新增能力（Prompt 审计、命令面板、安全加固、响应式、页脚公安网备）见 [CHANGELOG](../../CHANGELOG.md) 与 [docs/defense/evidence-matrix.md](../defense/evidence-matrix.md)。
