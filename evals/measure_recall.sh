#!/usr/bin/env bash
# 测量 ANN recall@K —— 技术方案 §5.7（不是 M5 的答案级命中率,那个测的
# 是"回答对不对",这个测的是"HNSW 近似索引漏掉了多少精确扫描能找到的分块"。
#
#   recall@K = |HNSW 返回的 top-K ∩ 精确扫描的 top-K| / K
#
# 用法：
#   export CONGORAG_DB_URL=postgres://postgres:postgres@127.0.0.1:5432/congorag?sslmode=disable
#   export CONGORAG_EMBED_BASE_URL=https://api.siliconflow.cn/v1
#   export CONGORAG_EMBED_API_KEY=sk-...
#   export CONGORAG_EMBED_MODEL=BAAI/bge-m3       # 必须和当前配置的 embedding 模型一致
#   ./evals/measure_recall.sh "查询文本"
#
# 这是给开发者手动跑的一次性测量脚本,不是产品功能的一部分——契约里
# 没有"跑一次 recall 测量"这个端点,发这个查询直接打真实的 embedding API。
set -euo pipefail

QUERY="${1:?用法: measure_recall.sh \"查询文本\"}"
: "${CONGORAG_DB_URL:?需要设置 CONGORAG_DB_URL}"
: "${CONGORAG_EMBED_BASE_URL:?需要设置 CONGORAG_EMBED_BASE_URL}"
: "${CONGORAG_EMBED_API_KEY:?需要设置 CONGORAG_EMBED_API_KEY}"
: "${CONGORAG_EMBED_MODEL:?需要设置 CONGORAG_EMBED_MODEL}"

# 【为什么不用 mktemp -d】在部分 Git-Bash/MinGW 安装上，mktemp 给出的
# 系统临时目录和这个 shell 里跑起来的 node/其它工具认为的临时目录
# 不是同一个位置（两者对 /tmp 的挂载点解析不一致）——脚本内部
# "curl 写文件 → node 读文件"这条链路会因此莫名其妙地找不到文件。
# 用仓库内的相对路径就没有这个歧义，跑完清掉即可。
SCRATCH="$(dirname "$0")/.recall-scratch"
mkdir -p "$SCRATCH"
trap 'rm -rf "$SCRATCH"' EXIT

# 【为什么用 --data-binary @文件,不用 -d "字符串"】实测过一次：中文查询词
# 直接当 shell 命令行参数传给 curl 时,在这套 MinGW/Git-Bash 环境上会被
# 静默改写,上游报"参数不合法"，但看起来完全正常，很难排查。写进文件
# 再用 --data-binary @file 读,绕开这一层。跨平台脚本遇到同类问题先怀疑
# 这个方向。
printf '{"model":"%s","input":"%s"}' "$CONGORAG_EMBED_MODEL" "$QUERY" > "$SCRATCH/req.json"

curl -s -X POST "$CONGORAG_EMBED_BASE_URL/embeddings" \
  -H "Authorization: Bearer $CONGORAG_EMBED_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @"$SCRATCH/req.json" \
  -o "$SCRATCH/resp.json"

# 用 node 把 JSON 里的向量拼成 pgvector 的文本字面量 "[0.1,0.2,...]"。
# 【为什么不用 python3】这台机器上 python3 是一个不工作的存根（调用即
# 返回 exit 49、没有任何输出）——见项目里其它脚本统一改用 node 的原因。
# 【用 fs.readFileSync 不用 require】require() 会把路径当模块名解析，
# 相对路径在不同的 node 版本/调用方式下解析基准不一致；直接读文件、
# 自己 JSON.parse 没有这个歧义。
QVEC=$(node -e "
const fs = require('fs');
const d = JSON.parse(fs.readFileSync('$SCRATCH/resp.json', 'utf-8'));
if (!d.data) { console.error('embedding request failed:', JSON.stringify(d)); process.exit(1); }
console.log('[' + d.data[0].embedding.join(',') + ']');
")

# 向量作为 SQL 字面量直接拼进 SQL 文件、走 stdin 喂给 psql——
# 不能当 docker exec / 命令行参数传（1024 维的浮点数文本超过了
# Windows 上 docker.exe 的 argv 长度上限,实测报 "Argument list too long"）。
run_query() {
  local mode="$1" limit="$2"
  cat > "$SCRATCH/query.sql" <<SQL
$( [ "$mode" = exact ] && echo "SET enable_indexscan = off; SET enable_bitmapscan = off;" || echo "SET hnsw.ef_search = 40;" )
SELECT id FROM document_chunks
WHERE embedding IS NOT NULL AND embedding_model = '$CONGORAG_EMBED_MODEL'
ORDER BY embedding <=> '$QVEC'::halfvec
LIMIT $limit;
SQL
  # 【psql 不能留在管道里】管道里 psql 的退出码和 grep 的"一行都没选中"都会
  # 让整条管道非 0，而脚本是 set -euo pipefail——两者混在一起，就分不出
  # "SQL 报错"和"结果本来就是空"。拆成两步：psql 的退出码单独判（报错就
  # 明确报错并退出），过滤只决定输出内容。
  # 【必须 -v ON_ERROR_STOP=1】没有它，SELECT 失败时 psql 的退出码仍是 0，
  # 错误只走 stderr，stdout 只剩两条 SET——过滤掉之后就变成"空结果"，于是
  # 报告里会出现一行看起来很正常的数字，而那个数字是在一个报错的查询上算的。
  if ! docker exec -i congorag-postgres psql -U postgres -d congorag -t -A -v ON_ERROR_STOP=1 \
      < "$SCRATCH/query.sql" > "$SCRATCH/rows.txt" 2> "$SCRATCH/psql-err.txt"; then
    echo "" >&2
    echo "✗ 查询失败（$mode, K=$limit），没有测到任何东西：" >&2
    sed 's/^/  /' "$SCRATCH/psql-err.txt" >&2
    echo "  · 确认迁移都跑过（document_chunks 表在不在、embedding_model 这个列名对不对）" >&2
    echo "  · 确认 CONGORAG_EMBED_MODEL 和索引时写入用的模型一致（当前：$CONGORAG_EMBED_MODEL）" >&2
    echo "  · 空库（表在、一行数据都没有）不会走到这里，那种情况会报 N/A" >&2
    exit 1
  fi
  # 【收尾要 `|| true`】grep 一行都没选中时返回 1；"结果为空"在这个脚本里
  # 是正常情形（刚 migrate up 完就是空库），不能让它终止脚本——否则下面那条
  # "N/A（分块数为 0）"分支永远走不到，用户看到的是半路无输出地死掉。
  grep -v '^SET$' "$SCRATCH/rows.txt" | grep -v '^$' || true
}

echo "K | recall@K | 精确扫描耗时 | ANN 耗时"
echo "--|----------|--------------|----------"
measured=0
for K in 5 10 20; do
  t0=$(date +%s%N)
  exact_ids=$(run_query exact "$K")
  t1=$(date +%s%N)
  ann_ids=$(run_query ann "$K")
  t2=$(date +%s%N)

  exact_ms=$(( (t1 - t0) / 1000000 ))
  ann_ms=$(( (t2 - t1) / 1000000 ))

  total=$(echo "$exact_ids" | grep -c . || true)
  hit=$(comm -12 <(echo "$exact_ids" | sort) <(echo "$ann_ids" | sort) | grep -c . || true)

  if [ "$total" -eq 0 ]; then
    recall="N/A（分块数为 0）"
  else
    measured=$(( measured + 1 ))
    recall=$(node -e "console.log((($hit/$total)*100).toFixed(1) + '%')")
  fi

  echo "$K | $recall | ${exact_ms}ms | ${ann_ms}ms"
done

# 【一行都没测到就不能算"跑成功了"】这个脚本产出的是一组 recall 数字。分母
# 为 0 时那张表里全是 N/A——它是一次**没做成的**测量，不是"recall 是 100%"
# 也不是"没问题"。退出码必须把这两种情形分开，否则 CI 或人只看 $? 就会把
# 空库/模型名写错当成通过（这正是 issue 里说的"退出码不是显式的失败"）。
if [ "$measured" -eq 0 ]; then
  echo "" >&2
  echo "✗ 一行都没测到：document_chunks 里没有 embedding_model = '$CONGORAG_EMBED_MODEL' 的分块。" >&2
  echo "  上面那张表全是 N/A，它不构成一次 recall 测量。先确认迁移跑过、文档已索引、" >&2
  echo "  CONGORAG_EMBED_MODEL 和写入时用的模型是同一个。" >&2
  exit 1
fi
