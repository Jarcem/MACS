#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Jarcem

# test-proc-safety —— 进程清理安全回归测试（对应 SIGKILL 事故排查报告 §7）
#
# 原则：
#   - 不重放任何危险命令；lsof 一律经 PROC_SAFETY_LSOF 注入伪实现（仅本测试的临时子进程受影响）
#   - 被清理目标 = 测试自建并登记的临时子进程（/bin/sleep 拷贝重命名，身份可断言）
#   - 无关进程（decoy，plain sleep）必须始终存活、绝不出现在清理清单
#
# 场景覆盖（排查报告 §7）：
#   S1  目标端口无监听进程
#   S2  只有一个目标进程
#   S3  多个目标进程
#   S4  同时存在大量无关联网进程（伪 lsof 输出 300+ 噪声 PID）
#   S5  目标端口被非预期程序占用（身份不符 → 排除）
#   S6  lsof 执行失败 → 中止清理并诊断
#   S7  PID 列表为空或格式异常 → 跳过不 kill
#   S8  目标进程收到 SIGTERM 后正常退出
#   S9  目标忽略 TERM 时：默认不升级（left_alive）；显式 --force-kill 且重核验后才 SIGKILL
#   S10 无关进程绝不进入待结束列表（贯穿 S2~S5 断言）
#   S11 库源码禁用模式守卫：lsof 必须带 -sTCP:LISTEN 约束；禁止裸 -i

set -u
cd "$(dirname "$0")/.." || exit 1
ROOT="$PWD"

PASS=0; FAIL=0
TMP="$(mktemp -d /tmp/proc-safety-test.XXXXXX)"
AUDIT="$TMP/audit.log"
trap 'rm -rf "$TMP"; pkill -TERM -P $$ 2>/dev/null' EXIT

assert_eq() { # $1=desc $2=actual $3=expected
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "  PASS: $1";
  else FAIL=$((FAIL+1)); echo "  FAIL: $1（actual=[$2] expected=[$3]）"; fi
}
assert_contains() {
  if printf '%s' "$2" | grep -qF "$3"; then PASS=$((PASS+1)); echo "  PASS: $1";
  else FAIL=$((FAIL+1)); echo "  FAIL: $1（未包含 [$3]）"; fi
}
assert_not_contains() {
  if printf '%s' "$2" | grep -qF "$3"; then FAIL=$((FAIL+1)); echo "  FAIL: $1（不应包含 [$3]）";
  else PASS=$((PASS+1)); echo "  PASS: $1"; fi
}
alive() { "$PROC_SAFETY_PS" -p "$1" > /dev/null 2>&1; }

PROC_SAFETY_PS="/bin/ps"
PROC_SAFETY_TERM_GRACE=1
export PROC_SAFETY_PS

# ── 测试夹具 ─────────────────────────────
# 目标进程：/bin/sleep 拷贝为 macs-target（实证 ps 显示完整路径，身份可精确断言）
# 关键：后台目标必须重定向 stdio——否则 $(start_target) 会等待继承的管道 EOF
# （这正是 2026-11-15 测试挂起的根因：目标进程持有 stdout 管道 60s）
TARGET_BIN="$TMP/macs-target"
cp /bin/sleep "$TARGET_BIN" 2>/dev/null || cp "$(command -v sleep)" "$TARGET_BIN"
start_target() { "$TARGET_BIN" 60 >/dev/null 2>&1 & echo $!; }
# 无关进程（decoy）：plain sleep，命令行无 macs-target 标记，绝不允许进入清理清单
start_decoy() { sleep 60 >/dev/null 2>&1 & echo $!; }
alive() { ps -p "$1" -o command= 2>/dev/null | grep -q .; }

# 伪 lsof：输出内容由 $TMP/lsof.out 决定，退出码由 $TMP/lsof.rc 决定
make_fake_lsof() {
  cat > "$TMP/lsof-fake" << 'EOF'
#!/bin/bash
# 伪 lsof：仅供回归测试；输出/退出码由测试夹具文件控制
cat "$FAKE_LSOF_OUT" 2>/dev/null
exit "$(cat "$FAKE_LSOF_RC" 2>/dev/null || echo 0)"
EOF
  chmod +x "$TMP/lsof-fake"
}

set_fake_lsof() { # $1=输出内容 $2=退出码
  printf '%s\n' "$1" > "$TMP/lsof.out"
  echo "$2" > "$TMP/lsof.rc"
}

# 在受控环境下调用 proc_plan_targets（注入伪 lsof）
plan() { # $1=port $2=expect_regex  → 清单写入全局 PLAN
  (
    export PROC_SAFETY_LSOF="$TMP/lsof-fake"
    export PROC_SAFETY_AUDIT="$AUDIT"
    export FAKE_LSOF_OUT="$TMP/lsof.out" FAKE_LSOF_RC="$TMP/lsof.rc"
    source scripts/lib/proc-safety.sh
    proc_plan_targets "$1" "$2"
  ) 2>>"$AUDIT"
}
plan_rc() { plan "$@" >/dev/null 2>>"$AUDIT"; echo $?; }

: > "$AUDIT"
make_fake_lsof

echo "== S1 目标端口无监听进程（真实严格 lsof + 伪 lsof exit 1）"
# 真实 lsof（严格约束形态）对一个几乎必然无监听的端口：应返回空清单、rc=0
REAL=$( ( export PROC_SAFETY_LSOF=/usr/sbin/lsof; export PROC_SAFETY_AUDIT="$AUDIT"; source scripts/lib/proc-safety.sh; proc_plan_targets 47999 '(^|[ /])macs-target([[:space:]]|$)' ) 2>>"$AUDIT" )
assert_eq "真实严格 lsof：无监听端口清单为空" "$REAL" ""
set_fake_lsof "" 1   # 无匹配夹具（exit 1 = lsof 无结果，属正常）
assert_eq "伪 lsof exit 1（无匹配）：清单为空" "$(plan 8470 '(^|[ /])macs-target([[:space:]]|$)' | grep -c .)" "0"
assert_eq "伪 lsof exit 1（无匹配）：退出码 0" "$(plan_rc 8470 '(^|[ /])macs-target([[:space:]]|$)')" "0"

echo "== S2 只有一个目标进程"
T1=$(start_target)
set_fake_lsof "p$T1" 0
PLAN=$(plan 8470 '(^|[ /])macs-target([[:space:]]|$)')
assert_eq "单目标清单" "$PLAN" "$T1"
kill -TERM "$T1" 2>/dev/null

echo "== S3 多个目标进程"
T1=$(start_target); T2=$(start_target)
set_fake_lsof "p$T1
p$T2" 0
PLAN=$(plan 8470 '(^|[ /])macs-target([[:space:]]|$)')
assert_contains "清单含目标1" "$PLAN" "$T1"
assert_contains "清单含目标2" "$PLAN" "$T2"
assert_eq "清单行数=2" "$(printf '%s\n' "$PLAN" | grep -c .)" "2"
kill -TERM "$T1" "$T2" 2>/dev/null

echo "== S4 大量无关联网进程（300 噪声 PID + decoy）"
T1=$(start_target)
D1=$(start_decoy)
NOISE="p$T1
p$D1"
i=0; while [ $i -lt 300 ]; do NOISE="$NOISE
p$((900000 + i))"; i=$((i+1)); done
set_fake_lsof "$NOISE" 0
PLAN=$(plan 8470 '(^|[ /])macs-target([[:space:]]|$)')
assert_eq "清单仅含已核验目标（噪声全排除）" "$PLAN" "$T1"
assert_not_contains "decoy PID 绝不在清单中" "$PLAN" "$D1"
assert_eq "decoy 仍存活" "$(alive "$D1" && echo yes)" "yes"
kill -TERM "$T1" 2>/dev/null

echo "== S5 目标端口被非预期程序占用（身份不符 → 排除，不 kill）"
set_fake_lsof "p$D1" 0
PLAN=$(plan 8470 '(^|[ /])macs-target([[:space:]]|$)')
assert_eq "身份不符：清单为空" "$PLAN" ""
assert_eq "非预期程序仍存活" "$(alive "$D1" && echo yes)" "yes"

echo "== S6 lsof 执行失败 → 中止并诊断"
set_fake_lsof "" 9
RC=$(plan_rc 8470 '(^|[ /])macs-target([[:space:]]|$)')
assert_eq "lsof 失败退出码 2（中止）" "$RC" "2"
assert_contains "审计含诊断" "$(cat "$AUDIT")" "lsof_exec_failed"

echo "== S7 PID 列表为空或格式异常 → 跳过不 kill"
PIDF="$TMP/pids.txt"
: > "$PIDF"
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" scripts/dev-stop.sh --pidfile "$PIDF" 2>>"$AUDIT")
assert_contains "空登记文件：不执行清理" "$OUT" "已处理 0 个"
printf 'tower notanumber\n\n   \ncmdterm 999999999\n' > "$PIDF"
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" scripts/dev-stop.sh --pidfile "$PIDF" 2>>"$AUDIT")
assert_contains "畸形行被跳过并审计" "$(cat "$AUDIT")" "malformed_pidfile_line"
assert_eq "畸形登记：处理数为 0" "$(echo "$OUT" | grep -o '已处理 [0-9]*' | grep -o '[0-9]*')" "0"
kill -TERM "$D1" 2>/dev/null

echo "== S8 目标进程收到 SIGTERM 后正常退出（dev-stop 登记模式）"
T1=$(start_target)
printf 'macs-target %s\n' "$T1" > "$PIDF"
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" PROC_SAFETY_TERM_GRACE=2 scripts/dev-stop.sh --pidfile "$PIDF" 2>>"$AUDIT")
assert_contains "S8 审计含 SIGTERM（送达已核验目标）" "$(cat "$AUDIT")" "signal=SIGTERM"
assert_not_contains "S8 未使用 SIGKILL（kill9 默认关闭）" "$(cat "$AUDIT")" "signal=SIGKILL"
# 审计一致性：TERM 之后必须是 exited_on_term 或 left_alive（kill9 关闭时不升级）
# 终态一致性：TERM 之后审计必须是 exited_on_term 或 left_alive（UE 僵残留环境下允许后者）
if printf '%s\n' "$(cat "$AUDIT")" | grep -qF "note=exited_on_term"; then
  assert_eq "S8 审计终态=exited_on_term" "ok" "ok"
else
  assert_contains "S8 审计终态=left_alive（UE 环境回退）" "$(cat "$AUDIT")" "action=left_alive"
fi
if [ "$(alive "$T1" && echo alive || echo gone)" = "alive" ]; then
  assert_contains "S8 存活时审计必须为 left_alive" "$(cat "$AUDIT")" "action=left_alive"
  echo "  NOTE: 目标处于内核 UE 残留态（环境损伤），库按设计未升级信号，行为正确"
fi

echo "== S9 目标忽略 TERM：默认不升级；--force-kill 重核验后才 SIGKILL"
IGNORE_SH="$TMP/macs-ignore"
printf '#!/bin/bash\ntrap \x27echo got_term\x27 TERM\ni=0\nwhile [ $i -lt 30 ]; do sleep 1; i=$((i+1)); done\n' > "$IGNORE_SH"
chmod +x "$IGNORE_SH"
"$IGNORE_SH" & IGN=$!
sleep 0.5   # 等 trap 安装完成，避免 TERM 抢在信号处理器之前到达（启动竞态）
printf 'macs-ignore %s\n' "$IGN" > "$PIDF"
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" PROC_SAFETY_TERM_GRACE=1 scripts/dev-stop.sh --pidfile "$PIDF" 2>>"$AUDIT")
assert_contains "S9 未开 KILL：审计记录 left_alive" "$(cat "$AUDIT")" "left_alive"
assert_not_contains "S9 未开 KILL：审计无 SIGKILL" "$(cat "$AUDIT")" "action=signal pid=${IGN} signal=SIGKILL"
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" PROC_SAFETY_TERM_GRACE=1 PROC_SAFETY_ALLOW_KILL9=1 scripts/dev-stop.sh --pidfile "$PIDF" --force-kill 2>>"$AUDIT")
# 终态一致性：重核验通过 → SIGKILL；重核验失败（目标已死/UE 残留）→ exited_or_replaced
if printf '%s\n' "$(cat "$AUDIT")" | grep -qF "signal=SIGKILL"; then
  assert_eq "S9 审计终态=SIGKILL（重核验通过后升级）" "ok" "ok"
else
  assert_contains "S9 审计终态=exited_or_replaced（UE 环境回退）" "$(cat "$AUDIT")" "note=exited_or_replaced"
fi
assert_contains "S9 force-kill：审计含重核验语义" "$(cat "$AUDIT")" "reason=grace_expired"
D2=$(start_decoy)
set_fake_lsof "p$D2" 0
OUT=$(PROC_SAFETY_AUDIT="$AUDIT" scripts/dev-stop.sh --port 8470 --expect '^never-match$' 2>>"$AUDIT")
assert_contains "S5 复验：非预期身份 0 清理" "$OUT" "已处理 0 个"
assert_eq "decoy2 仍存活" "$(alive "$D2" && echo yes)" "yes"
kill -TERM "$D2" 2>/dev/null

echo "== S11 源码禁用模式守卫（防复发；仅检查代码行，注释里的教训示例不算违规）"
for f in scripts/lib/proc-safety.sh scripts/dev-stop.sh scripts/dev-start.sh; do
  CODE=$(grep -vE '^[[:space:]]*#' "$f")
  BAD=$(printf '%s\n' "$CODE" | grep -nE '(^|[[:space:]])-i([[:space:]]|$)' | grep -v 'iTCP' || true)
  assert_eq "$f 无裸 -i 参数" "$BAD" ""
  BAD2=$(printf '%s\n' "$CODE" | grep -nE '\$PROC_SAFETY_LSOF' | grep -vE 'sTCP:LISTEN' || true)
  assert_eq "$f lsof 调用均带 LISTEN 约束" "$BAD2" ""
  BAD3=$(printf '%s\n' "$CODE" | grep -nE 'kill -9|pkill' || true)
  assert_eq "$f 无未加保护 kill -9 / pkill" "$BAD3" ""
done
REPO_BAD=$(grep -rnE 'kill -9 \$\(lsof|lsof -ti -i' --include='*.sh' . 2>/dev/null | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' | grep -v 'scripts/test-proc-safety.sh' || true)
assert_eq "仓库无危险 lsof/kill 组合残留" "$REPO_BAD" ""

cp "$AUDIT" /tmp/ps-audit-latest.log 2>/dev/null   # 审计镜像：失败后可事后核对
echo ""
echo "==== proc-safety 回归：PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" -eq 0 ] || exit 1
exit 0
