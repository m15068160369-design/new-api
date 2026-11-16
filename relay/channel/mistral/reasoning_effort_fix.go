package mistral

import "strings"

// [patch lou #18] reasoning_effort 合法化（Mistral 渠道专用）。
//
// 背景：补丁 #13 在 openai 通用路径把非法 effort 归 max，但 Mistral 类型渠道走
// 本包自己的 ConvertRequest，#16 让客户端值原样直通，绕过了 #13。实测 ch1
// (PLAN-mistral-Vibe) 两个上游模型合法值集不同：
//   - zai-glm-5-3:        {low, high, max}          → 'none' 400
//   - deepseek-v4-1-flash-1017: {none, low, high, xhigh, max} → 'medium' 400
// 客户端（Hermes 标题调用发 none、主循环发 medium）不合规值直接被上游 400 退回。
//
// 策略：per-model 已知值集优先；未知模型用 Mistral 全集 {low, high, max} 保守
// 处理。合法值透传；非法值就近语义映射（不砍思考强度，只升或等档）：
//   medium→high, minimal→low, xhigh→high, default→high,
//   none→low（模型不认 none 时；模型认 none 则透传）,
//   其余垃圾值→max。
// 空值不动（omitempty 不发字段，走上游默认）。
func normalizeReasoningEffort(effort, modelName string) string {
	if effort == "" {
		return ""
	}
	e := strings.ToLower(strings.TrimSpace(effort))

	if allowsEffortNone(modelName) {
		// 该模型接受 none（如 deepseek-v4-1-flash-1017）
		switch e {
		case "none", "low", "high", "xhigh", "max":
			return e
		case "medium", "default":
			return "high"
		case "minimal":
			return "low"
		}
		return "max"
	}

	// 保守集合 {low, high, max}（如 zai-glm-5-3 及未知模型）
	switch e {
	case "low", "high", "max":
		return e
	case "none", "minimal":
		return "low"
	case "medium", "default", "xhigh":
		return "high"
	}
	return "max"
}

// allowsEffortNone 报告该上游模型是否接受 reasoning_effort:"none"。
// 实测（2026-11-16 03 生产库 logs）：
//   - deepseek-v4-1-flash-1017 报 "supported values: ['none','low','high','xhigh','max']"
//   - zai-glm-5-3 报 "supported values: ['low','high','max']"
// 新模型上线后实测其 400 报文再维护此清单。
func allowsEffortNone(modelName string) bool {
	switch modelName {
	case "deepseek-v4-1-flash-1017":
		return true
	}
	return false
}
