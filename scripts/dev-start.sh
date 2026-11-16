#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Jarcem

# dev-start —— 本地开发守护进程启动（写 PID 登记文件，供 dev-stop 定向清理）
#
# 用法：
#   scripts/dev-start.sh [tower] [incubator] [cmdterm]
# 环境变量：
#   MACS_DEV_PIDFILE     PID 登记文件（默认 /tmp/macs-dev.pids）
#   MACS_DEV_DIR         工作目录（默认 /tmp/macs-dev，DB/日志放这里，不污染仓库）
#   MACS_MIGRATIONS_DIR  塔侧迁移目录（默认仓库内 migrations/）
#   MACS_INCUBATOR_MIGRATIONS_DIR
#   其余 MACS_* 变量透传给各组件
#
# 只登记本次脚本启动的进程；dev-stop 仅清理登记且身份核验通过的条目。

set -u
cd "$(dirname "$0")/.." || exit 1
REPO_ROOT="$PWD"
# shellcheck source=lib/proc-safety.sh
source scripts/lib/proc-safety.sh

PIDFILE="${MACS_DEV_PIDFILE:-/tmp/macs-dev.pids}"
DEV_DIR="${MACS_DEV_DIR:-/tmp/macs-dev}"
mkdir -p "$DEV_DIR"

# 已登记且存活的进程不重复启动（幂等）
already_running() { # $1=name
  # 局部变量名不得与调用方重名：bash 动态作用域 + read EOF 清空会覆写调用方的 local name
  local rname rpid
  while read -r rname rpid; do
    [ "$rname" = "$1" ] && proc_verify_identity "$rpid" "(^|[ /])$1([[:space:]]|$)" && return 0
  done < <(ps_load_pidfile "$PIDFILE")
  return 1
}

register() { # $1=name $2=pid
  printf '%s %s\n' "$1" "$2" >> "$PIDFILE"
  ps_audit "register" "name=${1}" "pid=${2}"
}

start_component() { # $1=name
  local name="$1"
  if already_running "$name"; then
    ps_audit "info" "reason=already_running" "name=${name}"
    return 0
  fi
  local moddir
  case "$name" in
    tower)     moddir="macs-tower" ;;
    incubator) moddir="macs-incubator" ;;
    cmdterm)   moddir="macs-cmdterm" ;;
  esac
  ( cd "$moddir" && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" go build -o "$DEV_DIR/$name" "./cmd/$name" ) || {
    ps_audit "error" "reason=go_build_failed" "name=${name}"; return 1;
  }
  case "$name" in
    tower)
      ( cd "$DEV_DIR" && MACS_MIGRATIONS_DIR="${MACS_MIGRATIONS_DIR:-$REPO_ROOT/macs-tower/migrations}" \
        "$DEV_DIR/tower" > "$DEV_DIR/tower.log" 2>&1 & echo $! > "$DEV_DIR/.pid.tower" )
      ;;
    incubator)
      ( cd "$DEV_DIR" && MACS_INCUBATOR_MIGRATIONS="${MACS_INCUBATOR_MIGRATIONS_DIR:-$REPO_ROOT/macs-incubator/src/db/migrations}" \
        MACS_INCUBATOR_NAME="${MACS_INCUBATOR_NAME:-dev-incubator}" \
        "$DEV_DIR/incubator" > "$DEV_DIR/incubator.log" 2>&1 & echo $! > "$DEV_DIR/.pid.incubator" )
      ;;
    cmdterm)
      if [ -z "${MACS_CMDTERM_SECRET:-}" ] || [ -z "${MACS_CMDTERM_ID:-}" ]; then
        ps_audit "skip" "reason=cmdterm_needs_secret" "note=register via POST /api/v1/command-terminals first"
        return 0
      fi
      ( cd "$DEV_DIR" && MACS_CMDTERM_ALLOW="${MACS_CMDTERM_ALLOW:-whoami,hostname,uname}" \
        "$DEV_DIR/cmdterm" > "$DEV_DIR/cmdterm.log" 2>&1 & echo $! > "$DEV_DIR/.pid.cmdterm" )
      ;;
    *)
      ps_audit "error" "reason=unknown_component" "name=${name}"; return 1 ;;
  esac
  local pid
  pid=$(cat "$DEV_DIR/.pid.$name" 2>/dev/null)
  ps_is_numeric "$pid" && register "$name" "$pid"
}

touch "$PIDFILE"
rc=0
for comp in "$@"; do
  start_component "$comp" || rc=1
done
echo "pidfile=$PIDFILE"
exit "$rc"
