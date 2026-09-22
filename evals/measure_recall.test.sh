#!/usr/bin/env bash
# measure_recall.sh 的自测（issue #127）。
#
# 【为什么用假 docker / 假 curl】这要验的是脚本的**管道与退出码**：
# "psql 成功但一行都没有"（空库）与"psql 失败"必须走出两条不同的路，而这条
# 分叉以前被 grep 的退出码吃掉了——两种情形都会让赋值语句在 set -e 下终止
# 脚本，于是 "N/A（分块数为 0）" 那条分支永远到不了。
# 要搭起真 Postgres + 真 embedding API 才能跑的话，这个回归就永远不会有人跑。
# 假 docker 只回答脚本提的那一个问题，正好把那几条路都造出来。
#
# 用法（不需要网络、不需要 docker、不需要数据库）：
#   ./evals/measure_recall.test.sh      # 退出码 0 = 全部符合预期
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
if [ ! -f "$HERE/measure_recall.sh" ]; then
  echo "找不到 $HERE/measure_recall.sh" >&2
  exit 1
fi

STUBS="$(mktemp -d)"
OUT="$(mktemp)"
ERR="$(mktemp)"
trap 'rm -rf "$STUBS"; rm -f "$OUT" "$ERR"' EXIT

# ── 假命令 ──────────────────────────────────────────────────────

cat > "$STUBS/docker" <<'STUB'
#!/usr/bin/env bash
# 只回答 measure_recall.sh 的 `exec -i ... psql ...`。STUB_MODE 决定演哪种库。
cat > /dev/null      # 读完喂进来的 query.sql（不读的话上游会吃到 SIGPIPE）
case "${STUB_MODE:-empty}" in
  error)             # 查询本身失败（列名写错、表不存在）
    echo 'ERROR:  column "embedding_model" does not exist' >&2
    exit 1
    ;;
  empty)             # 两条 SET 成功、查询成功、一行都没有：刚 migrate up 完
    printf 'SET\nSET\n'
    ;;
  rows)              # 两条 SET 成功 + 三行结果
    printf 'SET\nSET\n1\n2\n3\n'
    ;;
  *)
    echo "stub docker: 未知的 STUB_MODE=${STUB_MODE}" >&2
    exit 9
    ;;
esac
STUB

cat > "$STUBS/curl" <<'STUB'
#!/usr/bin/env bash
# 假装调了一次 embedding API：把 -o 指的路径写成一份带向量的响应。
out=""; prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
if [ -z "$out" ]; then
  echo 'stub curl: 没在参数里看到 -o' >&2
  exit 9
fi
printf '{"data":[{"embedding":[0.1,0.2,0.3]}]}' > "$out"
STUB

chmod +x "$STUBS/docker" "$STUBS/curl"

# ── 断言小工具 ──────────────────────────────────────────────────

FAILED=0
fail() {
  echo "  ✗ $1" >&2
  FAILED=1
}
has() { # has <文件> <子串> <说明>
  if grep -qF -- "$2" "$1"; then
    echo "  ✓ $3"
  else
    fail "$3 —— 期望在 $(basename "$1") 里看到「$2」"
  fi
}
hasnt() { # hasnt <文件> <子串> <说明>
  if grep -qF -- "$2" "$1"; then
    fail "$3 —— 不该在 $(basename "$1") 里看到「$2」"
  else
    echo "  ✓ $3"
  fi
}
expect_status() { # expect_status <期望：zero|nonzero> <实际退出码> <说明>
  if [ "$1" = zero ] && [ "$2" -eq 0 ]; then
    echo "  ✓ $3"
  elif [ "$1" = nonzero ] && [ "$2" -ne 0 ]; then
    echo "  ✓ $3"
  else
    fail "$3 —— 期望 $1，实际退出码 $2"
  fi
}

# 【必须 cd 进去、用相对路径调脚本】脚本自己用 `dirname "$0"` 拼出
# .recall-scratch，再把这个路径交给 node 去读 resp.json。在 Git-Bash 下用
# 绝对路径（/d/05_Code/...）调它的话，node 会把它当 Windows 路径解析成
# D:\d\05_Code\...，于是"curl 写了、node 读不到"——那正是脚本里那段关于
# mktemp 的注释描述的坑。相对路径就没有这个歧义，而且和 README 里的用法一致。
run_case() { # run_case <STUB_MODE>，退出码写到全局 STATUS
  local mode="$1"
  STATUS=0
  (
    cd "$HERE"
    STUB_MODE="$mode" \
      CONGORAG_DB_URL='postgres://stub/stub' \
      CONGORAG_EMBED_BASE_URL='https://example.invalid/v1' \
      CONGORAG_EMBED_API_KEY='sk-stub' \
      CONGORAG_EMBED_MODEL='stub-model' \
      PATH="$STUBS:$PATH" \
      bash ./measure_recall.sh '测试查询'
  ) > "$OUT" 2> "$ERR" || STATUS=$?
}

# ── 场景 1：空库（表在、一行数据都没有）─────────────────────────
#
# 这是 issue 的原始症状：脚本半路无输出地死掉，那张表永远看不到。

echo '场景 1：空库 —— 要给出 N/A，且退出码说明"没测成"'
run_case empty
has    "$OUT" 'N/A（分块数为 0）' '表里写着 N/A（分块数为 0）'
has    "$OUT" '5 | N/A'          'K=5 这一行确实打印出来了'
has    "$ERR" '一行都没测到'      'stderr 解释了为什么不算一次测量'
expect_status nonzero "$STATUS"  '退出码非 0（没数据可测 ≠ 测通过了）'

# ── 场景 2：SQL 报错 ────────────────────────────────────────────
#
# 必须和"空库"分开：报错时**不能**输出 N/A（那会让人以为库是空的），
# 也不能退出码为 0。

echo '场景 2：查询失败 —— 要明确报错，不能伪装成空库'
run_case error
has    "$ERR" '查询失败'            'stderr 写明是查询失败'
has    "$ERR" 'embedding_model'     'psql 的原始错误被带出来了'
has    "$ERR" '空库（表在、一行数据都没有）不会走到这里' '与空库情形做了区分'
hasnt  "$OUT" 'N/A'                 'stdout 里不出现 N/A（那会被当成空库）'
expect_status nonzero "$STATUS"     '退出码非 0'

# ── 场景 3：正常有数据 ──────────────────────────────────────────
#
# 前两条修好之后，正常路径不能被改坏：三次 K 都测出来，退出码 0。

echo '场景 3：有数据 —— 正常算出 recall，退出码 0'
run_case rows
has    "$OUT" '100.0%'          '精确扫描与 ANN 返回同一批 id → recall 100.0%'
expect_status zero "$STATUS"    '退出码 0'

echo ''
if [ "$FAILED" -ne 0 ]; then
  echo '✗ 有用例不符合预期' >&2
  exit 1
fi
echo '✓ measure_recall.sh 的三个场景都符合预期'
