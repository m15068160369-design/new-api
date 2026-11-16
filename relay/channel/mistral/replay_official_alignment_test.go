package mistral

// [patch lou #15b] 官方对齐回放测试矩阵：
// 1) chunklet 不带 closed/signature（openapi default:true，官方 Vibe 回放省略）
// 2) 工具轮同样回放 thinking（thinking+text+tool_calls 同条消息）
// 3) 无正文的纯工具轮回放 thinking-only chunklet
// 4) 纯空白占位（Hermes echo 协议 " " 垫）不建 thinking 块
// 5) 无思考的 assistant 轮保持原样不被转换（回归保护）

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

func validToolCallsJSON(id string) json.RawMessage {
	return json.RawMessage(`[{"id":"` + id + `","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"hz\"}"}}]`)
}

func decodeToolCalls(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad tool_calls json: %v (%s)", err, string(raw))
	}
	return out
}

func TestBuildAssistantChunkletDropsClosedAndOptionalText(t *testing.T) {
	chunks := buildAssistantChunklet("推理", "回答")
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	think, _ := chunks[0].(map[string]any)
	if think["type"] != "thinking" {
		t.Errorf("first chunk type wrong: %#v", think)
	}
	if _, has := think["closed"]; has {
		t.Errorf("closed must be omitted (openapi default:true, official omits): %#v", think)
	}
	if _, has := think["signature"]; has {
		t.Errorf("signature must be omitted: %#v", think)
	}
	text, _ := chunks[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "回答" {
		t.Errorf("text chunk wrong: %#v", text)
	}

	only := buildAssistantChunklet("推理", "")
	if len(only) != 1 {
		t.Fatalf("thinking-only should yield 1 chunk, got %d", len(only))
	}
	if only[0].(map[string]any)["type"] != "thinking" {
		t.Errorf("thinking-only chunk wrong: %#v", only[0])
	}
}

func TestRequestReplayThinkingOnToolCallTurn(t *testing.T) {
	rc := "调用前的思考"
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "天气怎么样"},
			{Role: "assistant", Content: "先查一下", ReasoningContent: &rc, ToolCalls: validToolCallsJSON("abc123def")},
			{Role: "tool", ToolCallId: "abc123def", Content: `{"temp":20}`},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	msg := out.Messages[1]
	if msg.Role != "assistant" {
		t.Fatalf("role wrong: %s", msg.Role)
	}
	arr, ok := msg.Content.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("tool-call turn should replay thinking+text chunklet, got %#v", msg.Content)
	}
	think, _ := arr[0].(map[string]any)
	if think["type"] != "thinking" {
		t.Errorf("first chunk should be thinking: %#v", think)
	}
	if _, has := think["closed"]; has {
		t.Errorf("closed must be omitted: %#v", think)
	}
	tcs := decodeToolCalls(t, msg.ToolCalls)
	if tcs[0]["id"] != "abc123def" {
		t.Errorf("tool call id should survive, got %#v", tcs[0])
	}
	fn, _ := tcs[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool function lost: %#v", tcs[0])
	}
	if out.Messages[2].ToolCallId != "abc123def" {
		t.Errorf("tool row tool_call_id should survive: %s", out.Messages[2].ToolCallId)
	}
}

func TestRequestReplayThinkingToolCallTurnNoText(t *testing.T) {
	rc := "纯工具轮思考"
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "天气怎么样"},
			{Role: "assistant", Content: "", ReasoningContent: &rc, ToolCalls: validToolCallsJSON("xyz987654")},
			{Role: "tool", ToolCallId: "xyz987654", Content: `{"temp":20}`},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	msg := out.Messages[1]
	arr, ok := msg.Content.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("text-less tool turn should replay thinking-only chunklet, got %#v", msg.Content)
	}
	if arr[0].(map[string]any)["type"] != "thinking" {
		t.Errorf("thinking-only chunk wrong: %#v", arr[0])
	}
	tcs := decodeToolCalls(t, msg.ToolCalls)
	if tcs[0]["id"] != "xyz987654" {
		t.Errorf("tool call id should survive, got %#v", tcs[0])
	}
}

func TestRequestBlankPadNotChunked(t *testing.T) {
	rc := " "
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "问题"},
			{Role: "assistant", Content: "回答", ReasoningContent: &rc},
			{Role: "user", Content: "追问"},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	raw, _ := json.Marshal(out.Messages[1].Content)
	s := string(raw)
	if strings.Contains(s, "thinking") {
		t.Errorf("blank pad must not build a thinking chunk, got %s", s)
	}
	if !strings.Contains(s, "回答") {
		t.Errorf("visible text lost, got %s", s)
	}
}

func TestRequestNoReasoningAssistantUntouched(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "问题"},
			{Role: "assistant", Content: "回答", ToolCalls: validToolCallsJSON("qwe789asd")},
			{Role: "tool", ToolCallId: "qwe789asd", Content: `{"x":1}`},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	raw, _ := json.Marshal(out.Messages[1].Content)
	if strings.Contains(string(raw), "thinking") {
		t.Errorf("no-reasoning tool turn must not gain a thinking chunk, got %s", raw)
	}
}
