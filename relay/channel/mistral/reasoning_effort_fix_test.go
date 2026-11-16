package mistral

import "testing"

// [patch lou #18] 单测矩阵：per-model 合法值集 + 非法值就近映射。
// 值集来源：2026-11-16 03 生产库 logs 实测 400 报文。
func TestNormalizeReasoningEffort(t *testing.T) {
	glm := "zai-glm-5-3" // 只认 low/high/max
	ds := "deepseek-v4-1-flash-1017" // 认 none/low/high/xhigh/max
	unknown := "some-future-model"

	cases := []struct {
		name, effort, model, want string
	}{
		// zai-glm-5-3：合法透传
		{"glm low", "low", glm, "low"},
		{"glm high", "high", glm, "high"},
		{"glm max", "max", glm, "max"},
		// zai-glm-5-3：非法映射（Hermes 标题调用 none、主循环 medium 是两大实测来源）
		{"glm none→low", "none", glm, "low"},
		{"glm medium→high", "medium", glm, "high"},
		{"glm minimal→low", "minimal", glm, "low"},
		{"glm xhigh→high", "xhigh", glm, "high"},
		{"glm default→high", "default", glm, "high"},
		{"glm garbage→max", "banana", glm, "max"},
		// deepseek：none 合法透传，medium→high
		{"ds none", "none", ds, "none"},
		{"ds low", "low", ds, "low"},
		{"ds xhigh", "xhigh", ds, "xhigh"},
		{"ds max", "max", ds, "max"},
		{"ds medium→high", "medium", ds, "high"},
		{"ds minimal→low", "minimal", ds, "low"},
		{"ds garbage→max", "banana", ds, "max"},
		// 未知模型：保守集合 {low,high,max}
		{"unknown low", "low", unknown, "low"},
		{"unknown none→low", "none", unknown, "low"},
		{"unknown medium→high", "medium", unknown, "high"},
		{"unknown garbage→max", "banana", unknown, "max"},
		// 空值/大小写/空格
		{"empty", "", glm, ""},
		{"case MAX", "MAX", glm, "max"},
		{"trim", " high ", glm, "high"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeReasoningEffort(c.effort, c.model); got != c.want {
				t.Errorf("normalizeReasoningEffort(%q, %q) = %q, want %q", c.effort, c.model, got, c.want)
			}
		})
	}
}
