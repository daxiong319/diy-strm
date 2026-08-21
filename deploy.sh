#!/usr/bin/env bash
# diy-strm 一键推送 + 自动部署脚本（Windows Git Bash 环境）
# 用法: ./deploy.sh "提交信息"
# 功能:
#   1. git add -A + commit（信息必填）
#   2. 自动检测可用代理推送 main（github.com 直连常被重置）
#   3. 轮询 GitHub Actions auto-deploy workflow 直到完成，打印结果
# 环境变量: GH_TOKEN（可选，未设置时用匿名 API，仓库公开即可）

set -u
cd "$(dirname "$0")"

if [ $# -lt 1 ]; then
  echo "用法: ./deploy.sh \"提交信息\"" >&2
  exit 1
fi
MSG="$1"

# ---------- 1. 检查工作区 ----------
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "错误: 不在 git 仓库内" >&2
  exit 1
fi

if [ -z "$(git status --porcelain)" ]; then
  echo "工作区干净，无提交内容"
  exit 1
fi

# ---------- 2. 提交 ----------
echo "==> git add -A && commit"
git add -A
if ! git commit -m "$MSG"; then
  echo "提交失败" >&2
  exit 1
fi
COMMIT_SHA=$(git rev-parse HEAD)
SHORT_SHA=${COMMIT_SHA:0:12}
echo "提交完成: $SHORT_SHA"

# ---------- 3. 推送（自动选择代理） ----------
push_cmd="git push origin main"
# 测试代理端口连通性
PROXY=""
for port in 7890 7897; do
  if timeout 2 bash -c "echo > /dev/tcp/127.0.0.1/$port" 2>/dev/null; then
    PROXY="http://127.0.0.1:$port"
    break
  fi
done
if [ -n "$PROXY" ]; then
  echo "==> 检测到本地代理 $PROXY，使用代理推送"
  push_cmd="git -c http.proxy=$PROXY -c https.proxy=$PROXY push origin main"
else
  echo "==> 未检测到本地代理，直连推送"
fi

if ! $push_cmd; then
  echo "推送失败！已提交但未推送: $SHORT_SHA" >&2
  echo "可手动重试: git push origin main" >&2
  exit 1
fi
echo "推送成功: $SHORT_SHA"

# ---------- 4. 轮询 auto-deploy workflow ----------
REPO="daxiong319/diy-strm"
API_BASE="https://api.github.com"
AUTH_HEADER=()
if [ -n "${GH_TOKEN:-}" ]; then
  AUTH_HEADER=(-H "Authorization: Bearer $GH_TOKEN")
fi
CURL_ARGS=(-s --max-time 30 -x "$PROXY" "${AUTH_HEADER[@]}")

echo "==> 等待 auto-deploy workflow 完成..."
# 推送后 Actions 有 ~10s 触发延迟，先等触发再查
sleep 15
RUN_ID=""
for i in $(seq 1 60); do
  RUNS=$(curl "${CURL_ARGS[@]}" "$API_BASE/repos/$REPO/actions/workflows/auto-deploy.yaml/runs?per_page=5" 2>/dev/null)
  RUN_ID=$(echo "$RUNS" | python -c "
import json,sys
try:
    d = json.load(sys.stdin)
    for r in d.get('workflow_runs', []):
        if r.get('head_sha','').startswith('$SHORT_SHA') or r.get('head_sha','') == '$COMMIT_SHA':
            print(r['id'])
            break
except Exception:
    pass
" 2>/dev/null)
  if [ -n "$RUN_ID" ]; then break; fi
  echo "  (等待 workflow 触发... ${i}s)"
  sleep 10
done

if [ -z "$RUN_ID" ]; then
  echo "未找到对应 workflow run（可能在限流或触发失败），可到 GitHub Actions 页面查看"
  exit 1
fi
echo "==> 找到 workflow run: $RUN_ID"

for i in $(seq 1 90); do
  RUN=$(curl "${CURL_ARGS[@]}" "$API_BASE/repos/$REPO/actions/runs/$RUN_ID" 2>/dev/null)
  STATUS=$(echo "$RUN" | python -c "import json,sys; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)
  CONCLUSION=$(echo "$RUN" | python -c "import json,sys; print(json.load(sys.stdin).get('conclusion',''))" 2>/dev/null)
  if [ "$STATUS" = "completed" ]; then
    echo ""
    if [ "$CONCLUSION" = "success" ]; then
      echo "✅ 部署成功! run $RUN_ID conclusion=$CONCLUSION"
      echo "   服务器容器已更新，可在 http://134.185.85.200:12333 验证"
    else
      echo "❌ 部署失败! run $RUN_ID conclusion=$CONCLUSION"
      echo "   详情: https://github.com/$REPO/actions/runs/$RUN_ID"
    fi
    # 打印各 job 结论
    curl "${CURL_ARGS[@]}" "$API_BASE/repos/$REPO/actions/runs/$RUN_ID/jobs" 2>/dev/null | python -c "
import json,sys
try:
    for j in json.load(sys.stdin).get('jobs', []):
        print('   job:', j['name'], '=>', j['conclusion'])
except Exception:
    pass
"
    exit $([ "$CONCLUSION" = "success" ] && echo 0 || echo 1)
  fi
  echo "  (部署进行中... ${STATUS} ${i}s)"
  sleep 15
done

echo "等待超时，请到 https://github.com/$REPO/actions/runs/$RUN_ID 查看"
exit 1
