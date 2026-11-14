# scripts/ —— 进程安全清理工具链

> 背景：2026-11-15 排查确认的批量 SIGKILL 事故。当时使用
> `kill -9 $(lsof -ti -i :PORT)` 清理冒烟进程，其中**裸 `-i`** 会选择全部联网
> socket 并与端口条件取并集，把无关进程（ZCode、ClashX、系统服务）的 PID
> 一并传给 `kill -9`，造成批量误杀。本工具链把「核验、登记、审计、升级门槛」
> 固化为脚本与回归测试，防止同类事故复发。

## 一览

| 脚本 | 用途 |
| --- | --- |
| `scripts/dev-start.sh` | 启动本地开发守护进程（tower / incubator / cmdterm），**写 PID 登记文件** |
| `scripts/dev-stop.sh` | 定向清理：默认只清理登记且身份核验通过的进程 |
| `scripts/lib/proc-safety.sh` | 安全清理核心库（规则见下） |
| `scripts/test-proc-safety.sh` | 回归测试（37 项断言，覆盖排查报告 §7 全场景） |

## 快速用法

```bash
# 启动（构建 + 运行 + 登记 PID；幂等，已在跑的组件跳过）
scripts/dev-start.sh tower incubator cmdterm

# 停止（默认 SIGTERM + 宽限重核验；不升级信号）
scripts/dev-stop.sh

# 强制升级 SIGKILL（仅对重核验后仍存活的登记进程，需显式允许）
scripts/dev-stop.sh --force-kill

# 定向端口模式（必须同时给身份正则；仍为 LISTEN 限定 + 逐 PID 核验）
scripts/dev-stop.sh --port 8470 --expect '(^|[ /])tower([[:space:]]|$)'
```

- PID 登记文件：`MACS_DEV_PIDFILE`（默认 `/tmp/macs-dev.pids`，每行 `名称 PID`）
- 工作目录/数据库/日志：`MACS_DEV_DIR`（默认 `/tmp/macs-dev`，不污染仓库）
- 已退出/身份不符的登记条目：跳过并审计，清理后自动从登记文件移除

## 安全规则（硬性，回归测试守卫）

| # | 规则 |
| --- | --- |
| R1 | `lsof` 只允许 `-nP -iTCP:<port> -sTCP:LISTEN` 严格形态；执行失败（rc>1）必须中止清理并输出诊断 |
| R2 | 每个候选 PID 逐一身份核验（`ps -o command=` 匹配期望正则），未通过者排除，绝不进入清理清单 |
| R3 | 默认 SIGTERM + 宽限重核验；SIGKILL 需显式 `--force-kill` 且 `PROC_SAFETY_ALLOW_KILL9=1`，升级前重新核验 |
| R4 | 首选启动时登记的 PID 文件；端口扫描仅作显式请求下的定向辅助 |
| R5 | 全程结构化审计：端口、候选、核验结果、信号、最终动作 |
| R6 | PID 恒等防御：跳过 PID 0/1/自身/父进程；非数字 PID 直接丢弃 |

审计行示例（stderr 输出，可经 `PROC_SAFETY_AUDIT=<file>` 同步落盘）：

```
[proc-safety] ts=... action=target  name=macs-target pid=19507 source=pidfile
[proc-safety] ts=... action=signal  pid=19507 signal=SIGTERM label=macs-target
[proc-safety] ts=... action=left_alive pid=19507 reason=grace_expired_and_kill9_disabled label=macs-target
```

## 回归测试

```bash
bash scripts/test-proc-safety.sh    # 37 项断言；使用伪 lsof 注入 + 隔离临时子进程
```

覆盖排查报告 §7 全场景：无监听 / 单目标 / 多目标 / 300+ 噪声 PID / 端口被
非预期程序占用 / lsof 失败中止 / 畸形 PID 清单 / SIGTERM 正常退出 / 忽略
TERM 时默认不升级、force-kill 重核验后才 SIGKILL / 无关进程贯穿断言（绝不
出现在清单且始终存活）/ 源码禁用模式守卫（S11：无裸 `-i`、lsof 必带
LISTEN 约束、无未加保护 `kill -9`/`pkill`，注释行豁免）。

## 环境注意事项

若曾经历进程误杀（本仓库 2026-11-15 事故波及系统服务），个别进程可能卡在
内核 `UE`（不可中断退出）态——SIGTERM/SIGKILL 均无法回收，`ps` 持续可见。
这是内核级残留损伤，**重启机器**是唯一可靠的清理方式。库对此类环境的处理：
如实审计 `left_alive`（kill9 关闭）或 `signal=SIGKILL`（显式允许）后保留现状，
不扩大清理范围。
