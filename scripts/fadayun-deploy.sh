#!/usr/bin/env bash
# fadayun-deploy.sh — 本地编排脚本
# 用法: bash scripts/fadayun-deploy.sh <push|pull|build|upgrade|promote|rollback|all>
set -euo pipefail

REMOTE_HOST="fadayun"
REMOTE_SRC="/data/new-api-src"
REMOTE_SCRIPT="$REMOTE_SRC/scripts/fadayun-remote.sh"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}[deploy]${NC} $*"; }
ok()    { echo -e "${GREEN}[ok]${NC} $*"; }
warn()  { echo -e "${YELLOW}[warn]${NC} $*"; }
die()   { echo -e "${RED}[error]${NC} $*" >&2; exit 1; }

remote() { ssh "$REMOTE_HOST" "bash $REMOTE_SCRIPT $*"; }

cmd_push() {
  info "检查未提交变更..."
  if ! git -C "$(git rev-parse --show-toplevel)" diff --quiet HEAD; then
    die "存在未提交变更，请先 commit 或 stash"
  fi
  info "推送到 origin/main..."
  git push origin main
  ok "push 完成"
}

cmd_pull()     { info "远程拉取代码...";   remote pull;    ok "pull 完成"; }
cmd_build()    { info "远程构建镜像...";   remote build;   ok "build 完成"; }
cmd_upgrade()  { info "灰度升级 (10%)..."; remote upgrade; ok "upgrade 完成"; }
cmd_promote()  { info "全量发布...";       remote promote; ok "promote 完成"; }
cmd_rollback() { info "回滚...";           remote rollback; ok "rollback 完成"; }

cmd_all() {
  cmd_push
  cmd_pull
  cmd_build
  cmd_upgrade
}

case "${1:-}" in
  push)     cmd_push ;;
  pull)     cmd_pull ;;
  build)    cmd_build ;;
  upgrade)  cmd_upgrade ;;
  promote)  cmd_promote ;;
  rollback) cmd_rollback ;;
  all)      cmd_all ;;
  *) echo "用法: $0 <push|pull|build|upgrade|promote|rollback|all>"; exit 1 ;;
esac
