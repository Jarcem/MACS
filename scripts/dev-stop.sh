#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Jarcem

# dev-stop —— 本地开发守护进程定向清理
#
# 用法：
#   scripts/dev-stop.sh [--pidfile F] [--force-kill] [--port N --expect REGEX]
#
# 模式：
#   1) PID 登记模式（默认）：只清理 dev-start.sh 登记且身份核验通过的进程。
#      登记文件缺失/为空 → 不执行任何 kill（目标服务未运行时不 kill）。
#   2) 定向端口模式（可选，必须同时给 --expect 身份正则）：
#      严格 `lsof -nP -iTCP:<N> -sTCP:LISTEN` + 逐 PID 身份核验，
#      核验不通过的候选一律排除并审计。绝不使用裸 `-i` 全机扫描。
#
# 默认 SIGTERM（宽限 PROC_SAFETY_TERM_GRACE 秒）；宽限后仍存活且需强制时
# 必须显式 --force-kill（内部置 PROC_SAFETY_ALLOW_KILL9=1）并重新核验身份。

set -u
cd "$(dirname "$0")/.." || exit 1
# shellcheck source=lib/proc-safety.sh
source scripts/lib/proc-safety.sh

PIDFILE="${MACS_DEV_PIDFILE:-/tmp/macs-dev.pids}"
FORCE_KILL=0
PORT_MODE=0
PORT=""
EXPECT=""

while [ $# -gt 0 ]; do
  case "$1" in
    --pidfile)  PIDFILE="$2"; shift 2 ;;
    --force-kill) FORCE_KILL=1; shift ;;
    --port)     PORT="${2:-}"; PORT_MODE=1; shift 2 ;;
    --expect)   EXPECT="${2:-}"; shift 2 ;;
    *) echo "[dev-stop] 未知参数：$1" >&2; exit 2 ;;
  esac
done

if [ "$FORCE_KILL" = "1" ]; then
  PROC_SAFETY_ALLOW_KILL9=1
fi

# ── 模式 1：PID 登记文件（权威清单）──
stopped=0
while read -r name pid; do
  # 每个登记条目独立核验：进程名必须与登记名一致
  if proc_verify_identity "$pid" "(^|[ /])${name}([[:space:]]|$)"; then
    ps_audit "target" "name=${name}" "pid=${pid}" "source=pidfile"
    proc_stop_verified "$pid" "(^|[ /])${name}([[:space:]]|$)" "$name"
    stopped=$((stopped + 1))
  else
    ps_audit "skip" "name=${name}" "pid=${pid}" "reason=identity_mismatch_or_gone"
  fi
done < <(ps_load_pidfile "$PIDFILE")

# 清理已退出的登记行（保留仍存活的，便于诊断）
if [ -s "$PIDFILE" ]; then
  tmp="$PIDFILE.tmp"
  : > "$tmp"
  while read -r name pid; do
    proc_verify_identity "$pid" "(^|[ /])${name}([[:space:]]|$)" && printf '%s %s\n' "$name" "$pid" >> "$tmp"
  done < <(ps_load_pidfile "$PIDFILE")
  mv "$tmp" "$PIDFILE"
fi

# ── 模式 2：定向端口（显式请求 + 身份正则必填）──
if [ "$PORT_MODE" = "1" ]; then
  if [ -z "$EXPECT" ]; then
    echo "[dev-stop] 端口模式必须同时提供 --expect <身份正则>（拒绝无核验清理）" >&2
    exit 2
  fi
  targets=$(proc_plan_targets "$PORT" "$EXPECT") || { echo "[dev-stop] lsof 失败，中止清理" >&2; exit 2; }
  for pid in $targets; do
    proc_stop_verified "$pid" "$EXPECT" "port=$PORT"
    stopped=$((stopped + 1))
  done
fi

ps_audit "summary" "stopped=${stopped}" "pidfile=${PIDFILE}"
echo "[dev-stop] 已处理 ${stopped} 个登记/核验进程（审计见 stderr${PROC_SAFETY_AUDIT:+ 或 $PROC_SAFETY_AUDIT}）"
exit 0
