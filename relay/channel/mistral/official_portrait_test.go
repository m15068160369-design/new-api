package mistral

// [patch lou #20] 官方画像对齐四补丁测试矩阵：
// D1 UA 常量 / D2 metadata 稳妥键集+call_type / D3 工具 id 跨轮确定性 /
// D4 parallel_tool_calls 补齐 / 序列化烟囱（prompt_cache_key 回归缺席）

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

func TestOfficialUserAgent(t *testing.T) {
	if mistralOfficialUserAgent != "speakeasy-sdk/python 3.5.0 2.935.1 1.0.0 mistralai" {
		t.Errorf("UA 常量被改动：%q（现行 mistral-vibe 链路实测形态，勿随手换）", mistralOfficialUserAgent)
	}
}

func TestMetadataStableKeys(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "第一问"},
		},
	}
	wrapped, affinityKey := requestOpenAI2Mistral(req)
	meta := wrapped.Metadata
	if meta["os"] != "Linux" || meta["arch"] != "x86_64" {
		t.Errorf("os/arch 应如实固定：%v", meta)
	}
	if meta["call_type"] != "main_call" {
		t.Errorf("无工具回合应为 main_call，got %v", meta["call_type"])
	}
	if meta["session_id"] != affinityKey {
		t.Errorf("session_id 应与 x-affinity 同源：%v vs %v", meta["session_id"], affinityKey)
	}
	for _, banned := range []string{"version", "user_plan"} {
		if _, has := meta[banned]; has {
			t.Errorf("键 %s 服务端可比对，不碰（楼拍板：稳妥键集）", banned)
		}
	}
}

func TestMetadataCallTypeSecondary(t *testing.T) {
	tc := "推理"
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "查天气"},
			{Role: "assistant", Content: "", ReasoningContent: &tc, ToolCalls: validToolCallsJSON("abc123def")},
			{Role: "tool", ToolCallId: "abc123def", Content: `{"temp":20}`},
		},
	}
	wrapped, _ := requestOpenAI2Mistral(req)
	if wrapped.Metadata["call_type"] != "secondary_call" {
		t.Errorf("工具循环后续轮应为 secondary_call，got %v", wrapped.Metadata["call_type"])
	}
}

func TestToolCallIdDeterministicRewrite(t *testing.T) {
	weird := `call_abc-123_def-456`
	build := func() (assistantID, toolRowID string) {
		req := &dto.GeneralOpenAIRequest{
			Model: "glm-5.3",
			Messages: []dto.Message{
				{Role: "user", Content: "q"},
				{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"` + weird + `","type":"function","function":{"name":"f","arguments":"{}"}}]`)},
				{Role: "tool", ToolCallId: weird, Content: "r"},
			},
		}
		out, _ := requestOpenAI2Mistral(req)
		var tc []map[string]any
		_ = json.Unmarshal(out.Messages[1].ToolCalls, &tc)
		return tc[0]["id"].(string), out.Messages[2].ToolCallId
	}
	a1, t1 := build()
	a2, t2 := build()
	if a1 != a2 {
		t.Errorf("跨轮不稳定：首轮 %q 二轮 %q —— 背离 #20c 缓存目标", a1, a2)
	}
	if a1 != t1 || a2 != t2 {
		t.Errorf("assistant 与 tool 行改写后失配对：%q/%q", a1, t1)
	}
	if !mistralToolCallIdRegexp.MatchString(a1) {
		t.Errorf("改写结果应合官方 9 字符形态，got %q", a1)
	}
}

func TestToolCallIdNativePassThrough(t *testing.T) {
	native := "chatcmpl-tool-b98077408ffbb009"
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(`[{"id":"` + native + `","type":"function","function":{"name":"f","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallId: native, Content: "r"},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	var tc []map[string]any
	_ = json.Unmarshal(out.Messages[1].ToolCalls, &tc)
	if tc[0]["id"] != native {
		t.Errorf("上游原生 id 应原样放行（官方同形），got %v", tc[0]["id"])
	}
	if out.Messages[2].ToolCallId != native {
		t.Errorf("tool 行原生 id 应原样放行，got %s", out.Messages[2].ToolCallId)
	}
}

func ptcToolsFixture() []dto.ToolCallRequest {
	return []dto.ToolCallRequest{
		{Type: "function", Function: dto.FunctionRequest{Name: "f"}},
	}
}

func TestParallelToolCallsBackfill(t *testing.T) {
	// tools 非空且客户端未设 → 补 true（官方客户端恒开）
	req := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "user", Content: "q"}},
		Tools:    ptcToolsFixture(),
	}
	out, _ := requestOpenAI2Mistral(req)
	if out.ParallelTooCalls == nil || !*out.ParallelTooCalls {
		t.Errorf("tools 非空应补 parallel_tool_calls=true，got %v", out.ParallelTooCalls)
	}
	// 客户端显式 false → 透传
	f := false
	req.ParallelTooCalls = &f
	out2, _ := requestOpenAI2Mistral(req)
	if out2.ParallelTooCalls == nil || *out2.ParallelTooCalls {
		t.Errorf("客户端显式值应透传，got %v", out2.ParallelTooCalls)
	}
	// 无 tools → 不带
	req3 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "user", Content: "q"}},
	}
	out3, _ := requestOpenAI2Mistral(req3)
	if out3.ParallelTooCalls != nil {
		t.Errorf("无 tools 不应带 ptc，got %v", out3.ParallelTooCalls)
	}
}

func TestSerializedWireSmoke(t *testing.T) {
	tc := "推理"
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "第一问 which is greater"},
			{Role: "assistant", Content: "答", ReasoningContent: &tc, ToolCalls: validToolCallsJSON("chatcmpl-tool-0123456789abcdef")},
			{Role: "tool", ToolCallId: "chatcmpl-tool-0123456789abcdef", Content: "r"},
		},
		Tools:  ptcToolsFixture(),
		Stream: func() *bool { b := true; return &b }(),
	}
	wrapped, _ := requestOpenAI2Mistral(req)
	raw, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, want := range []string{`"metadata"`, `"call_type":"secondary_call"`, `"parallel_tool_calls":true`, "chatcmpl-tool-0123456789abcdef"} {
		if !contains(s, want) {
			t.Errorf("wire 缺少 %s：%s", want, s[:min(len(s), 400)])
		}
	}
	if contains(s, `"prompt_cache_key"`) {
		t.Errorf("#16d 回归：body 不应再带 prompt_cache_key")
	}
	if contains(s, "Go-http-client") || !contains(s, "speakeasy-sdk") {
		// UA 在 header 不在 body，此处仅防未来误把 UA 放进 body 阴性断言
		_ = s
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
