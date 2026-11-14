#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Jarcem

# proc-safety —— 进程安全清理库
#
# 事件教训（2026-11-15，sess_05ad4142）：
#   `kill -9 $(lsof -ti -i :PORT)` 中裸 `-i` 会选择全部联网 socket，
#   与后续 `-i :PORT` 条件形成并集，返回大量无关进程 PID，
#   被 kill -9 批量误杀（波及 ZCode / ClashX / 系统服务）。
#
# 本库的硬性规则：
#   R1. lsof 只允许 `-nP -iTCP:<port> -sTCP:LISTEN` 严格约束形态，禁止裸 `-i`；
#       lsof 执行失败（exit>1）必须中止并输出诊断，不得继续清理。
#   R2. 每个候选 PID 必须通过身份核验（进程名 basename 匹配期望正则），
#       未通过者只排除、绝不进入清理清单。
#   R3. 默认 SIGTERM；宽限期内未退出者需重新核验身份后才允许升级，
#       且 SIGKILL 必须显式开启 PROC_SAFETY_ALLOW_KILL9=1。
#   R4. 优先使用启动时登记的 PID 文件（只清理本次测试显式启动的进程），
#       端口扫描仅作为显式要求（--port + --expect）下的定向辅助。
#   R5. 全程结构化审计：目标端口、候选 PID、身份核验结果、信号、最终动作。
#   R6. PID 恒等防御：跳过 0/1/自身 PID；非数字 PID 直接丢弃。

PROC_SAFETY_LSOF="${PROC_SAFETY_LSOF:-/usr/sbin/lsof}"
PROC_SAFETY_PS="${PROC_SAFETY_PS:-/bin/ps}"
PROC_SAFETY_AUDIT="${PROC_SAFETY_AUDIT:-}"                    # 可选审计文件路径
PROC_SAFETY_ALLOW_KILL9="${PROC_SAFETY_ALLOW_KILL9:-0}"       # SIGKILL 默认关闭
PROC_SAFETY_TERM_GRACE="${PROC_SAFETY_TERM_GRACE:-5}"         # SIGTERM 宽限秒数

# ps_audit <action> <key=value>...
# 审计行始终输出到 stderr；设置了 PROC_SAFETY_AUDIT 时同步追加到文件
ps_audit() {
  local action="$1"; shift
  local kv="" a
  for a in "$@"; do kv+=" $a"; done
  local line
  line="[proc-safety] ts=$(date -u +%Y-%m-%dT%H:%M:%SZ) action=${action}${kv}"
  printf '%s\n' "$line" >&2
  if [ -n "$PROC_SAFETY_AUDIT" ]; then
    printf '%s\n' "$line" >> "$PROC_SAFETY_AUDIT"
  fi
}

# ps_is_numeric <value> —— 严格数字校验
ps_is_numeric() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  return 0
}

# ps_lsof_listeners <port>
#   列出端口 LISTEN 状态的 PID（每行一个）。只输出以 'p' 开头的纯数字行。
#   返回：0=正常（可能为空）；2=lsof 执行失败（调用方必须中止清理）
ps_lsof_listeners() {
  local port="$1"
  ps_is_numeric "$port" || { ps_audit "error" "reason=port_not_numeric" "value=${port}"; return 2; }
  local out rc
  out=$("$PROC_SAFETY_LSOF" -nP -iTCP:"$port" -sTCP:LISTEN -Fp 2>/dev/null)
  rc=$?
  if [ "$rc" -eq 0 ]; then
    printf '%s\n' "$out" | sed -n 's/^p\([0-9][0-9]*\)$/\1/p'
    return 0
  elif [ "$rc" -eq 1 ]; then
    return 0   # lsof 约定：无匹配返回 1，属正常
  fi
  ps_audit "error" "reason=lsof_exec_failed" "rc=${rc}" "port=${port}"
  return 2
}

# proc_verify_identity <pid> <expected_eregex>
#   身份核验：完整命令行（ps -o command=）匹配期望正则；
#   支持二进制路径（^.../tower$）与参数标记（bash -c ... macs-target）两种身份。
#   ps 失败或进程已退出 → 不通过
proc_verify_identity() {
  local pid="$1" ere="$2"
  ps_is_numeric "$pid" || return 1
  local cmd
  cmd=$("$PROC_SAFETY_PS" -p "$pid" -o command= 2>/dev/null) || return 1
  [ -n "$cmd" ] || return 1
  printf '%s' "$cmd" | grep -Eq "$ere"
}

# proc_plan_targets <port> <expected_eregex>
#   生成"已核验待清理清单"：候选 → 逐一身份核验 → 只输出通过者的 PID。
#   返回：0=正常（清单可能为空）；2=lsof 失败（调用方中止清理）
proc_plan_targets() {
  local port="$1" ere="$2"
  local cands verified="" excluded="" pid
  if ! cands=$(ps_lsof_listeners "$port"); then
    return 2   # 诊断已由 ps_lsof_listeners 审计
  fi
  for pid in $cands; do
    if proc_verify_identity "$pid" "$ere"; then
      verified+=" $pid"
    else
      excluded+=" $pid"
    fi
  done
  ps_audit "plan" "port=${port}" "candidates=[${cands}]" "verified=[${verified}]" "excluded=[${excluded}]"
  printf '%s\n' $verified
  return 0
}

# proc_stop_verified <pid> <expected_eregex> [label]
#   定向停止单个已核验进程：
#   1) SIGTERM → 等待 TERM_GRACE 秒
#   2) 仍未退出 → 重新核验身份 → 通过且 PROC_SAFETY_ALLOW_KILL9=1 才 SIGKILL
#   3) 未通过重核验或未允许 KILL → 保留进程并审计（绝不扩大范围）
proc_stop_verified() {
  local pid="$1" ere="$2" label="${3:-target}"
  # PID 恒等防御：永不清除 init/自身/父 shell
  if [ "$pid" = "1" ] || [ "$pid" = "0" ] || [ "$pid" = "$$" ] || [ "$pid" = "$PPID" ]; then
    ps_audit "skip" "pid=${pid}" "reason=protected_pid" "label=${label}"
    return 0
  fi
  if ! proc_verify_identity "$pid" "$ere"; then
    ps_audit "skip" "pid=${pid}" "reason=identity_mismatch" "label=${label}"
    return 0
  fi
  ps_audit "signal" "pid=${pid}" "signal=SIGTERM" "label=${label}"
  kill -TERM "$pid" 2>/dev/null || { ps_audit "done" "pid=${pid}" "note=already_gone" "label=${label}"; return 0; }

  local waited=0 grace="$PROC_SAFETY_TERM_GRACE"
  while [ "$waited" -lt "$grace" ]; do
    proc_verify_identity "$pid" "$ere" || { ps_audit "done" "pid=${pid}" "note=exited_on_term" "label=${label}"; return 0; }
    sleep 1
    waited=$((waited + 1))
  done

  if proc_verify_identity "$pid" "$ere"; then
    if [ "$PROC_SAFETY_ALLOW_KILL9" = "1" ]; then
      ps_audit "signal" "pid=${pid}" "signal=SIGKILL" "reason=grace_expired" "label=${label}"
      kill -KILL "$pid" 2>/dev/null
      ps_audit "done" "pid=${pid}" "note=killed_after_grace" "label=${label}"
    else
      ps_audit "left_alive" "pid=${pid}" "reason=grace_expired_and_kill9_disabled" "label=${label}"
    fi
  else
    ps_audit "done" "pid=${pid}" "note=exited_or_replaced" "label=${label}"
  fi
  return 0
}

# ps_load_pidfile <pidfile>
#   读取登记文件（每行 `name pid`）；跳过空行/非法行并审计
#   输出：每行 `name pid`
ps_load_pidfile() {
  local file="$1" name pid ln=0
  if [ ! -s "$file" ]; then
    ps_audit "info" "reason=pidfile_empty_or_missing" "file=${file}"
    return 0
  fi
  while IFS= read -r line; do
    ln=$((ln + 1))
    name=${line%% *}
    pid=${line##* }
    ps_is_numeric "$pid" || { ps_audit "skip" "reason=malformed_pidfile_line" "line=${ln}"; continue; }
    [ -n "$name" ] || { ps_audit "skip" "reason=malformed_pidfile_name" "line=${ln}"; continue; }
    printf '%s %s\n' "$name" "$pid"
  done < "$file"
  return 0
}
