package quality

import (
	"encoding/json"
)

// V1.5 Agent 协作路径的自检报告（孵化器 worker 产出结构）
type agentSelfCheck struct {
	Status   string           `json:"status"`
	Findings []AgentFinding   `json:"findings"`
}

// AgentFinding 结构化自检发现
type AgentFinding struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// SelfCheckFromResultStrict V1.5 失败关闭自检提取（设计 §6.2）：
//   - 缺 self_check → failed（blocking finding）
//   - self_check.status 为空 → failed
// 与 2.x 孵化器兼容路径 SelfCheckFromResult（缺省 passed）严格分离。
func SelfCheckFromResultStrict(result string) SelfCheckReport {
	var wrapper struct {
		SelfCheck *agentSelfCheck `json:"self_check"`
	}
	if json.Unmarshal([]byte(result), &wrapper) != nil || wrapper.SelfCheck == nil {
		return SelfCheckReport{
			Status: "failed",
			Summary: "missing structured self_check",
			Items: []CheckItem{{Name: "self_check", Passed: false, Note: "missing structured self_check (fail-closed)"}},
		}
	}
	sc := wrapper.SelfCheck
	status := sc.Status
	if status == "" {
		status = "failed"
	}
	items := make([]CheckItem, 0, len(sc.Findings))
	for _, f := range sc.Findings {
		items = append(items, CheckItem{Name: f.Severity, Passed: f.Severity != "blocking", Note: f.Message})
	}
	return SelfCheckReport{Status: status, Items: items}
}
