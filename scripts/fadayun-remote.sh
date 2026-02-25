#!/usr/bin/env bash
# fadayun-remote.sh — 在 fadayun 上执行，由 fadayun-deploy.sh 通过 SSH 调用
# 用法: bash /data/new-api-src/scripts/fadayun-remote.sh <pull|build|upgrade|promote|rollback>
set -euo pipefail

SRC_DIR="/data/new-api-src"
COMPOSE_FILE="$SRC_DIR/docker-compose.fadayun.yml"
CADDY_CONF="/etc/caddy/conf.d/new-api.caddy"
HEALTH_A="http://127.0.0.1:3100/api/status"
HEALTH_B="http://127.0.0.1:3101/api/status"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
info() { echo -e "${CYAN}[remote]${NC} $*"; }
ok()   { echo -e "${GREEN}[ok]${NC} $*"; }
die()  { echo -e "${RED}[error]${NC} $*" >&2; exit 1; }

# ── Caddy upstream 切换 ─────────────────────────────────────────
# set_upstream <wa> <wb>  (0=禁用, 非0=启用)
set_upstream() {
  local wa=$1 wb=$2
  info "切换 Caddy upstream: a=$wa b=$wb"
  python3 - "$CADDY_CONF" "$wa" "$wb" <<'PYEOF'
import sys, re, pathlib
conf = pathlib.Path(sys.argv[1])
wa, wb = int(sys.argv[2]), int(sys.argv[3])
if wa == 0:
    new_to = "        to http://127.0.0.1:3101"
elif wb == 0:
    new_to = "        to http://127.0.0.1:3100"
else:
    new_to = "        to http://127.0.0.1:3100 http://127.0.0.1:3101"
text = re.sub(r'^\s+to http://127\.0\.0\.1:310[01].*$', new_to, conf.read_text(), flags=re.MULTILINE)
conf.write_text(text)
PYEOF
  caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy
  ok "Caddy 已重载 (a=$wa, b=$wb)"
}

# ── 健康检查 ────────────────────────────────────────────────────
wait_healthy() {
  local url=$1 name=$2 retries=12
  info "等待 $name 健康..."
  for i in $(seq 1 $retries); do
    if curl -sf "$url" | grep -q '"success":true'; then
      ok "$name 健康检查通过"
      return 0
    fi
    echo "  第 $i/$retries 次，等待 10s..."
    sleep 10
  done
  die "$name 健康检查超时"
}

# ── pull ────────────────────────────────────────────────────────
cmd_pull() {
  cd "$SRC_DIR"
  info "拉取最新代码..."
  git fetch origin
  git reset --hard origin/main
  ok "代码已更新到 $(git rev-parse --short HEAD)"
}

# ── build ───────────────────────────────────────────────────────
cmd_build() {
  cd "$SRC_DIR"
  local commit time
  commit=$(git rev-parse --short HEAD)
  time=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  info "构建 new-api:canary (commit=$commit)..."
  docker build \
    --tag new-api:canary \
    --label "git.commit=$commit" \
    --label "build.time=$time" \
    .
  ok "镜像构建完成: new-api:canary"
}

# ── upgrade（灰度 10%）─────────────────────────────────────────
cmd_upgrade() {
  cd "$SRC_DIR"
  info "备份当前镜像为 new-api:stable..."
  docker tag new-api:canary new-api:stable 2>/dev/null || true

  info "重建 new-api-b (灰度)..."
  TAG_B=canary docker compose -f "$COMPOSE_FILE" --env-file .env \
    up -d --no-deps --force-recreate new-api-b

  wait_healthy "$HEALTH_B" "new-api-b"
  set_upstream 90 10
  ok "灰度升级完成，new-api-b 承接 10% 流量"
}

# ── promote（全量）─────────────────────────────────────────────
cmd_promote() {
  cd "$SRC_DIR"
  info "切换全量流量到 new-api-b..."
  set_upstream 0 100

  info "重建 new-api-a (稳定版)..."
  TAG_A=canary docker compose -f "$COMPOSE_FILE" --env-file .env \
    up -d --no-deps --force-recreate new-api-a

  wait_healthy "$HEALTH_A" "new-api-a"
  set_upstream 90 10

  info "标记 canary → stable..."
  docker tag new-api:canary new-api:stable
  ok "全量发布完成，双实例均运行 canary 版本"
}

# ── rollback ────────────────────────────────────────────────────
cmd_rollback() {
  cd "$SRC_DIR"
  info "回滚：切换全量流量到 new-api-a..."
  set_upstream 100 0

  info "用 new-api:stable 重建 new-api-b..."
  TAG_B=stable docker compose -f "$COMPOSE_FILE" --env-file .env \
    up -d --no-deps --force-recreate new-api-b

  wait_healthy "$HEALTH_B" "new-api-b"
  set_upstream 90 10
  ok "回滚完成，new-api-b 已恢复 stable 版本"
}

case "${1:-}" in
  pull)     cmd_pull ;;
  build)    cmd_build ;;
  upgrade)  cmd_upgrade ;;
  promote)  cmd_promote ;;
  rollback) cmd_rollback ;;
  *) echo "用法: $0 <pull|build|upgrade|promote|rollback>"; exit 1 ;;
esac
