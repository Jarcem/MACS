# MACS 商业授权说明

> 本文件是 [AGPL-3.0](LICENSE) 之外**另行的商业授权（Commercial License）**入口说明。
> 本说明仅为授权范围的通俗解释，不构成法律意见；正式商业授权以双方签署的书面协议为准。

- 项目：MACS（Multi-Agent Cooperative System）
- 权利方：Jarcem
- 联系：`leo@leosindustry.com`
- SPDX：`AGPL-3.0-or-later`

---

## 一、AGPL-3.0 下你可以免费做什么

| 场景 | 是否需要商业授权 | 说明 |
| --- | --- | --- |
| 个人学习、研究、内部评估 | 否 | 直接使用即可 |
| 公司内部自用，不对外提供网络服务 | 否 | 按 GPL 系列常规边界执行 |
| 修改并开源你的衍生作品（AGPL 兼容） | 否 | 需保留版权声明并以 AGPL 公开源码 |
| 对外提供网络服务，且公开对应源码 | 否 | AGPL 第 13 条义务已满足 |
| 与 AGPL 兼容的开源项目集成 | 否 | 整体仍以 AGPL 分发 |

## 二、什么情况需要商业授权

只要你的使用方式**无法履行 AGPL-3.0 的义务**，就需要单独的商业授权：

1. **闭源集成** —— 把 MACS 或其修改版嵌入自有产品，且不愿公开对应源码。
2. **专有分发** —— 随闭源软件分发 MACS 二进制，或作为其子模块交付给客户。
3. **托管服务（SaaS / 私有云交付）** —— 对外提供 MACS 能力，但不向使用者提供对应源码。
4. **OEM / 白标** —— 以自有品牌再包装、再销售。
5. **去除 AGPL 传染性义务** —— 需要将 MACS 与不兼容许可的代码静态组合、单一二进制分发。
6. **合规豁免** —— 企业法务要求供应链中不出现 AGPL 组件。

> 典型判据：**你是否对外提供服务 / 分发软件，且不愿或不能公开修改后的源码？**
> 是 → 需要商业授权；否 → AGPL 免费授权即可。

## 三、商业授权范围（按需约定）

商业授权按用途与规模签署，通常覆盖：

- 闭源使用、修改、再分发（含子模块、OEM、白标）
- 约定的部署规模（节点数 / 孵化器数 / 并发任务数）与期限
- 可选的**源码托管（escrow）**、技术支持与 SLA
- 明确排除：商标使用权、专利授权（除非单独约定）

授权为**非独占、不可转授**，除非书面另有约定。

## 四、如何申请

发送邮件至 **`leo@leosindustry.com`**，请包含：

1. 公司 / 主体名称与所在地
2. 使用形态（内部自用 / SaaS / 私有化交付 / OEM）
3. 部署规模（预计节点数、任务量）
4. 是否需要修改源码、是否需要技术支持
5. 期望生效时间

通常在收到信息后回复授权方案与报价；签署后提供商业授权凭证（License Key / 授权函）。

## 五、其他说明

- **不可撤回**：已按 AGPL-3.0 发布的版本，其既有授权不能被收回；商业授权只影响后续版本与新增条款。
- **第三方依赖**：`modernc.org/sqlite`（BSD-3-Clause）、React / Vite / Ink（MIT）等仍适用各自许可，不由本项目的 AGPL 或商业授权覆盖，详见 [NOTICE](NOTICE)。
- **无担保**：除商业协议另有约定，软件按「现状」提供，不附带任何明示或默示担保。
- **贡献**：向本项目提交 PR 即表示同意以 AGPL-3.0 授权你的贡献；如需另行约定请提前说明。

---

# MACS Commercial Licensing (English)

> This file describes the **separate commercial license** available in addition to [AGPL-3.0](LICENSE).
> It is a plain-language summary, not legal advice; the signed written agreement governs.

You may use MACS free of charge under AGPL-3.0 as long as you can comply with its obligations
(most importantly: if you modify MACS and make it available to users over a network, you must
offer the corresponding source under AGPL).

A commercial license is required when you cannot comply, typically for:

1. **Closed-source integration** — embedding MACS or a modified version in a proprietary product
   without releasing the corresponding source.
2. **Proprietary redistribution** — shipping MACS binaries with closed-source software.
3. **Hosted service (SaaS / managed private cloud)** — offering MACS capabilities without providing
   the corresponding source to users.
4. **OEM / white-label** — rebranding or reselling under your own brand.
5. **Removing AGPL copyleft obligations** — static combination or single-binary distribution with
   license-incompatible code.
6. **Procurement exemptions** — enterprise policies that disallow AGPL components in the supply chain.

Commercial terms are agreed per use case and scale, and typically cover closed-source use,
modification, redistribution (including OEM/white-label), an agreed deployment scope and term,
and optional source escrow, support and SLA. Trademark and patent rights are excluded unless
separately agreed. The license is non-exclusive and non-transferable unless stated otherwise.

To request a commercial license, email **`leo@leosindustry.com`** with: entity name and location,
usage model (internal / SaaS / on-prem delivery / OEM), expected scale, whether you modify the
source or need support, and the desired effective date.

Notes: rights already granted for published versions cannot be revoked; third-party components
keep their own licenses (see [NOTICE](NOTICE)); the software is provided "as is" unless the
commercial agreement says otherwise; contributions are licensed under AGPL-3.0 unless agreed
otherwise in advance.

Copyright &copy; 2026 Jarcem
