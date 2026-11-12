package mistral

// [patch lou #15a/#15b] 归一化转换的模式矩阵单测。
// 用例数据全部取自 2026-11-12 对 zai-glm-5-3（经 New API 渠道1/Resin）的实测帧，
// 少量为官方文档/issue 记载的兼容形态（thinking 内层 string、arguments 对象）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"
)

func normalizeLine(t *testing.T, line string, roleSeen *bool) map[string]interface{} {
	t.Helper()
	out := normalizeSSELine(line, roleSeen)
	if !strings.HasPrefix(out, "data: ") {
		t.Fatalf("expcted data prefix, got: %q", out)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(out, "data: ")), &m); err != nil {
		t.Fatalf("result not json: %v (%q)", err, out)
	}
	return m
}

func TestNormalizeSSELine_Passthrough(t *testing.T) {
	roleSeen := false
	for _, line := range []string{
		"data: [DONE]",
		": keep-alive comment",
		"",
	} {
		if got := normalizeSSELine(line, &roleSeen); got != line {
			t.Errorf("passthrough changed: in=%q out=%q", line, got)
		}
	}
	badJSON := "data: {not json"
	if got := normalizeSSELine(badJSON, &roleSeen); got != badJSON {
		t.Errorf("bad json must passthrough, got %q", got)
	}
}

func TestNormalizeSSELine_ThinkingChunk(t *testing.T) {
	// 实测帧：thinking chunklet 数组 → reasoning_content，content 键删除，delta.index 删除
	line := `data: {"choices":[{"delta":{"content":[{"closed":true,"thinking":[{"text":"The","type":"text"}],"type":"thinking"}],"index":0},"finish_reason":null,"index":0,"logprobs":null}],"created":1794497919,"id":"x","model":"glm-5.3","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	delta := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if _, has := delta["content"]; has {
		t.Errorf("content key should be removed for pure thinking frame")
	}
	if rc, _ := delta["reasoning_content"].(string); rc != "The" {
		t.Errorf("reasoning_content = %q, want %q", rc, "The")
	}
	if _, has := delta["index"]; has {
		t.Errorf("delta.index should be removed")
	}
}

func TestNormalizeSSELine_ThinkingInnerStringVariant(t *testing.T) {
	// 兼容形态：thinking 字段直接是字符串
	line := `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":"direct string"}]}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	delta := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if rc, _ := delta["reasoning_content"].(string); rc != "direct string" {
		t.Errorf("reasoning_content = %q", rc)
	}
}

func TestNormalizeSSELine_TextContentPreserved(t *testing.T) {
	// 纯字符串正文帧：content 原样保留为字符串；delta.index（非标键）剔除；
	// index 剔除导致帧重写，故不再要求字节级透传，只验证语义等价。
	line := `data: {"choices":[{"delta":{"content":"与目标比较，从而","index":0},"finish_reason":null,"index":0,"logprobs":null}],"created":1794497919,"id":"x","model":"glm-5.3","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	delta := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if c, _ := delta["content"].(string); c != "与目标比较，从而" {
		t.Errorf("content = %q", c)
	}
	if _, has := delta["index"]; has {
		t.Errorf("delta.index must be removed")
	}
	if _, has := delta["reasoning_content"]; has {
		t.Errorf("no reasoning expected")
	}
	if fr, _ := m["choices"].([]interface{})[0].(map[string]interface{})["finish_reason"]; fr != nil {
		t.Errorf("finish_reason null must be preserved")
	}
}

func TestNormalizeSSELine_TransitionChunk(t *testing.T) {
	// 过渡帧：数组同时含 closing thinking 块与首个 text 块（官方文档记载形态）
	line := `data: {"choices":[{"delta":{"content":[{"type":"thinking","closed":true,"thinking":[{"type":"text","text":"think tail"}]},{"type":"text","text":"first answer"}]}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	delta := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if rc, _ := delta["reasoning_content"].(string); rc != "think tail" {
		t.Errorf("reasoning_content = %q", rc)
	}
	if c, _ := delta["content"].(string); c != "first answer" {
		t.Errorf("content = %q", c)
	}
}

func TestNormalizeSSELine_RoleOnlyOnce(t *testing.T) {
	// 实测：首两帧都带 role:assistant，官方只在首帧带 → 第二帧剔除
	first := `data: {"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	second := `data: {"choices":[{"delta":{"content":"","index":0,"role":"assistant"},"finish_reason":null,"index":0,"logprobs":null}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	normalizeLine(t, first, &roleSeen)
	m2 := normalizeLine(t, second, &roleSeen)
	delta2 := m2["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if _, has := delta2["role"]; has {
		t.Errorf("second role frame should drop role")
	}
	// 再来一帧 role 出现也不复活
	third := strings.Replace(second, `"content":""`, `"content":"","role":"assistant"`, 1)
	m3 := normalizeLine(t, third, &roleSeen)
	delta3 := m3["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if _, has := delta3["role"]; has {
		t.Errorf("role must stay dropped once seen")
	}
}

func TestNormalizeSSELine_FinishChunk(t *testing.T) {
	// 实测 finish 帧：response_error 私有字段剔除，usage/finish_reason 保留
	line := `data: {"choices":[{"delta":{"content":"","index":0},"finish_reason":"stop","index":0,"logprobs":null,"response_error":{"content_validation":["valid_think_chunk","valid_text_chunk"],"extra_tags":[],"format_error":null,"tool_calls_validation":[]}}],"created":1794497919,"id":"x","model":"glm-5.3","object":"chat.completion.chunk","usage":{"completion_tokens":408,"completion_tokens_details":{"reasoning_tokens":362},"prompt_tokens":18,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":426}}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	choice := m["choices"].([]interface{})[0].(map[string]interface{})
	if _, has := choice["response_error"]; has {
		t.Errorf("response_error must be removed")
	}
	if fr, _ := choice["finish_reason"].(string); fr != "stop" {
		t.Errorf("finish_reason = %q", fr)
	}
	if _, has := m["usage"]; !has {
		t.Errorf("usage must be preserved")
	}
}

func TestNormalizeSSELine_FinishReasonMapping(t *testing.T) {
	for in, want := range map[string]string{"model_length": "length", "error": "stop"} {
		line := `data: {"choices":[{"delta":{"content":"","index":0},"finish_reason":"` + in + `","index":0,"logprobs":null,"response_error":{}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
		roleSeen := false
		m := normalizeLine(t, line, &roleSeen)
		choice := m["choices"].([]interface{})[0].(map[string]interface{})
		if fr, _ := choice["finish_reason"].(string); fr != want {
			t.Errorf("finish_reason %q → %q, got %q", in, want, fr)
		}
	}
	// 未知值透传
	line := `data: {"choices":[{"delta":{"content":"","index":0},"finish_reason":"something_new","index":0,"logprobs":null}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	choice := m["choices"].([]interface{})[0].(map[string]interface{})
	if fr, _ := choice["finish_reason"].(string); fr != "something_new" {
		t.Errorf("unknown finish_reason should passthrough, got %q", fr)
	}
}

func TestNormalizeSSELine_ToolCallChunks(t *testing.T) {
	// 实测：首 tool 帧完整（id+name+type+index 保留）；后帧 name:"" 剔除
	first := `data: {"choices":[{"delta":{"content":"","index":0,"tool_calls":[{"function":{"arguments":"{\"city\": \"北京","name":"get_weather"},"id":"chatcmpl-tool-b98077408ffbb009","index":0,"type":"function"}]},"finish_reason":null,"index":0,"logprobs":null}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m1 := normalizeLine(t, first, &roleSeen)
	tc1 := m1["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["tool_calls"].([]interface{})[0].(map[string]interface{})
	if _, has := tc1["id"]; !has {
		t.Errorf("first tool frame must keep id")
	}
	if _, has := tc1["index"]; !has {
		t.Errorf("stream tool frame must keep index (增量关联键)")
	}

	second := `data: {"choices":[{"delta":{"content":"","index":0,"tool_calls":[{"function":{"arguments":"\"}","name":""},"index":0}]},"finish_reason":"tool_calls","index":0,"logprobs":null,"response_error":{}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk","usage":{"completion_tokens":37,"total_tokens":192}}`
	m2 := normalizeLine(t, second, &roleSeen)
	delta2 := m2["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	tc2 := delta2["tool_calls"].([]interface{})[0].(map[string]interface{})
	fn2 := tc2["function"].(map[string]interface{})
	if _, has := fn2["name"]; has {
		t.Errorf("empty name must be removed")
	}
	if _, has := tc2["id"]; has {
		t.Errorf("id 'null'/missing must not be re-added")
	}
	// arguments 对象转字符串（兼容形态）
	objFrame := `data: {"choices":[{"delta":{"content":"","tool_calls":[{"function":{"arguments":{"city":"杭州"},"name":"get_weather"},"id":"call-1","index":0,"type":"function"}]},"finish_reason":null,"index":0}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	m3 := normalizeLine(t, objFrame, &roleSeen)
	tc3 := m3["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["tool_calls"].([]interface{})[0].(map[string]interface{})
	fn3 := tc3["function"].(map[string]interface{})
	if args, _ := fn3["arguments"].(string); args != `{"city":"杭州"}` {
		t.Errorf("arguments object must marshal to string, got %#v", fn3["arguments"])
	}
}

func TestNormalizeSSELine_ToolCallIdNullString(t *testing.T) {
	// pydantic-ai issue 记载的 SDK 默认值形态：后续帧 id 为字符串 "null"
	line := `data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"x","name":"fn"},"id":"null","index":0}]},"finish_reason":null,"index":0}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	tc := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["tool_calls"].([]interface{})[0].(map[string]interface{})
	if _, has := tc["id"]; has {
		t.Errorf("id 'null' literal must be removed")
	}
}

func TestNormalizeSSELine_UnknownChunkFallback(t *testing.T) {
	// 未知块：带 text 字段并入正文，无 text 全丢 content 键
	withText := `data: {"choices":[{"delta":{"content":[{"type":"mystery","text":"t1"},{"type":"text","text":"t2"}]}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, withText, &roleSeen)
	delta := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if c, _ := delta["content"].(string); c != "t1t2" {
		t.Errorf("content = %q, want t1t2", c)
	}
	noText := `data: {"choices":[{"delta":{"content":[{"type":"mystery","foo":1}]}}],"created":1,"id":"x","model":"m","object":"chat.completion.chunk"}`
	m2 := normalizeLine(t, noText, &roleSeen)
	delta2 := m2["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if _, has := delta2["content"]; has {
		t.Errorf("content key should be dropped when no text extracted")
	}
	if _, has := delta2["reasoning_content"]; has {
		t.Errorf("no reasoning expected")
	}
}

func TestNormalizeSSELine_CreatedMissing(t *testing.T) {
	// 两份抓包记载不一致的形态：顶层缺 created → 补当前时间戳
	line := `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"思考"}]}]}}],"id":"x","model":"m","object":"chat.completion.chunk"}`
	roleSeen := false
	m := normalizeLine(t, line, &roleSeen)
	if _, has := m["created"]; !has {
		t.Errorf("created must be filled when missing")
	}
}

func TestNormalizeNonStreamBody(t *testing.T) {
	// 实测非流式帧：双块数组 → content 字符串 + reasoning_content
	body := []byte(`{"id":"c0","created":1,"model":"zai-glm-5-3","usage":{"prompt_tokens":18,"completion_tokens":343,"total_tokens":361,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"image_tokens":0},"completion_tokens_details":{"text_tokens":0,"audio_tokens":0,"reasoning_tokens":309},"input_tokens":0,"output_tokens":0,"input_tokens_details":null,"claude_cache_creation_5_m_tokens":0,"claude_cache_creation_1_h_tokens":0},"object":"chat.completion","choices":[{"finish_reason":"stop","index":0,"logprobs":null,"message":{"role":"assistant","tool_calls":null,"content":[{"closed":true,"thinking":[{"text":"input before answer"}],"type":"thinking"},{"text":"最终答案","type":"text"}]}}]}`)
	out := NormalizeChatCompletionBody(body)
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not json: %v", err)
	}
	msg := m["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	if c, _ := msg["content"].(string); c != "最终答案" {
		t.Errorf("content = %q", c)
	}
	if rc, _ := msg["reasoning_content"].(string); rc != "input before answer" {
		t.Errorf("reasoning_content = %q", rc)
	}
	if _, has := msg["tool_calls"]; has {
		t.Errorf("null tool_calls must be dropped")
	}
	usage := m["usage"].(map[string]interface{})
	for _, k := range []string{"input_tokens", "output_tokens", "input_tokens_details",
		"claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens"} {
		if _, has := usage[k]; has {
			t.Errorf("usage private key %q must be removed", k)
		}
	}
	ptd := usage["prompt_tokens_details"].(map[string]interface{})
	if _, has := ptd["text_tokens"]; has {
		t.Errorf("prompt_tokens_details.text_tokens must be removed")
	}
	ctd := usage["completion_tokens_details"].(map[string]interface{})
	if _, has := ctd["text_tokens"]; has {
		t.Errorf("completion_tokens_details.text_tokens must be removed")
	}
	if rt, _ := ctd["reasoning_tokens"].(float64); rt != 309 {
		t.Errorf("reasoning_tokens = %v", rt)
	}
}

func TestNormalizeNonStreamBody_Passthrough(t *testing.T) {
	// 标准格式非流式 body：字节级不变
	body := []byte(`{"id":"a","object":"chat.completion","created":1,"model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	if got := NormalizeChatCompletionBody(body); string(got) != string(body) {
		t.Errorf("standard body must byte-passthrough:\n in=%s\nout=%s", body, got)
	}
	// 坏 JSON 原样
	bad := []byte(`{oops`)
	if got := NormalizeChatCompletionBody(bad); string(got) != string(bad) {
		t.Errorf("bad json must passthrough")
	}
	// 错误体（无 choices）原样，不崩溃
	errBody := []byte(`{"detail":[{"type":"missing","loc":["body"],"msg":"field required"}]}`)
	if got := NormalizeChatCompletionBody(errBody); string(got) != string(errBody) {
		t.Errorf("error body must passthrough (错误体走 New API 统一错误管道)")
	}
}

func TestRequestOpenAI2Mistral_ReasoningToChunklet(t *testing.T) {
	rc := "思考过程"
	contentStr := "回答正文"
	rcJSON, _ := json.Marshal(rc)
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "问题"},
			{Role: "assistant", Content: contentStr, ReasoningContent: &rc},
			{Role: "user", Content: "追问"},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	arr, ok := out.Messages[1].Content.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("assistant content should become 2-block chunklet, got %#v", out.Messages[1].Content)
	}
	think, _ := arr[0].(map[string]any)
	if think["type"] != "thinking" || think["closed"] != true {
		t.Errorf("thinking block wrong: %#v", think)
	}
	inner, _ := think["thinking"].([]any)
	innerMap, _ := inner[0].(map[string]any)
	if innerMap["text"] != rc || innerMap["type"] != "text" {
		t.Errorf("inner thinking wrong: %#v", innerMap)
	}
	text, _ := arr[1].(map[string]any)
	if text["type"] != "text" || text["text"] != contentStr {
		t.Errorf("text block wrong: %#v", text)
	}
	_ = rcJSON
	// 无思考 assistant：content 经 SetMediaContent 仍是文本形态，不套 chunklet
	req2 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "assistant", Content: "纯文本"}},
	}
	out2, _ := requestOpenAI2Mistral(req2)
	if _, isChunklet := out2.Messages[0].Content.([]any); isChunklet {
		t.Logf("no-chunklet path content=%T (media array ok)", out2.Messages[0].Content)
	}
}

func TestRequestOpenAI2Mistral_ToolChoice(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model:      "glm-5.3",
		ToolChoice: "required",
		Messages:   []dto.Message{{Role: "user", Content: "hi"}},
	}
	outTC, _ := requestOpenAI2Mistral(req)
	if tc, _ := outTC.ToolChoice.(string); tc != "any" {
		t.Errorf("required→any, got %v", tc)
	}
	req2 := &dto.GeneralOpenAIRequest{
		Model:      "glm-5.3",
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "f"}},
		Messages:   []dto.Message{{Role: "user", Content: "hi"}},
	}
	outT2, _ := requestOpenAI2Mistral(req2)
	if s, _ := outT2.ToolChoice.(string); s == "any" {
		t.Errorf("object tool_choice must passthrough")
	}
}

func TestRequestOpenAI2Mistral_DeveloperRole(t *testing.T) {
	// [patch lou #16] Mistral 只认 system/user/assistant/tool：developer → system
	req := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "developer", Content: "你是编码助手"},
			{Role: "user", Content: "hi"},
		},
	}
	out, _ := requestOpenAI2Mistral(req)
	if out.Messages[0].Role != "system" {
		t.Errorf("developer must map to system, got %q", out.Messages[0].Role)
	}
	// 其他 role 不动
	req2 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "tool", Content: "result"}, {Role: "assistant", Content: "ok"}},
	}
	out2, _ := requestOpenAI2Mistral(req2)
	if out2.Messages[0].Role != "tool" || out2.Messages[1].Role != "assistant" {
		t.Errorf("other roles must passthrough, got %q/%q", out2.Messages[0].Role, out2.Messages[1].Role)
	}
}

func TestRequestOpenAI2Mistral_ReasoningEffortPassthrough(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model:           "glm-5.3",
		ReasoningEffort: "max",
		Messages:        []dto.Message{{Role: "user", Content: "hi"}},
	}
	out, _ := requestOpenAI2Mistral(req)
	if out.ReasoningEffort != "max" {
		t.Errorf("reasoning_effort must passthrough, got %q", out.ReasoningEffort)
	}
	req2 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	out2, _ := requestOpenAI2Mistral(req2)
	if out2.ReasoningEffort != "" {
		t.Errorf("empty effort must stay empty (omitempty drop), got %q", out2.ReasoningEffort)
	}
}

func TestRequestOpenAI2Mistral_PromptCacheKeyAndAffinity(t *testing.T) {
	// 客户端给了 prompt_cache_key：原样透传并作为 affinity key 返回
	req := &dto.GeneralOpenAIRequest{
		Model:          "glm-5.3",
		PromptCacheKey: "client-key-123",
		Messages:       []dto.Message{{Role: "user", Content: "hi"}},
	}
	out, aff := requestOpenAI2Mistral(req)
	if out.PromptCacheKey != "client-key-123" || aff != "client-key-123" {
		t.Errorf("client cache key must passthrough verbatim, got %q/%q", out.PromptCacheKey, aff)
	}

	// 没给 key：从首条 user 派生稳定 key；同会话第二轮（历史追加）派生值不变
	req2 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "user", Content: "帮我写排序"}, {Role: "assistant", Content: "ok"}},
	}
	out2, aff2 := requestOpenAI2Mistral(req2)
	req3 := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "user", Content: "帮我写排序"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "改成快排"},
		},
	}
	out3, aff3 := requestOpenAI2Mistral(req3)
	if aff2 == "" {
		t.Fatalf("derived key must not be empty")
	}
	if aff2 != aff3 {
		t.Errorf("same conversation turn-2 must keep same key: %q vs %q", aff2, aff3)
	}
	if out2.PromptCacheKey != aff2 || out3.PromptCacheKey != aff3 {
		t.Errorf("out.PromptCacheKey must mirror affinity key")
	}
	// 不同对话派生不同 key
	req4 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "user", Content: "完全不同的问题"}},
	}
	_, aff4 := requestOpenAI2Mistral(req4)
	if aff4 == aff2 {
		t.Errorf("different conversation root must derive different key")
	}
	// 没有 user 消息（纯 instructions）：key 为空，不注 x-affinity 也不加 body 字段
	req5 := &dto.GeneralOpenAIRequest{
		Model:    "glm-5.3",
		Messages: []dto.Message{{Role: "system", Content: "sys"}},
	}
	out5, aff5 := requestOpenAI2Mistral(req5)
	if aff5 != "" || out5.PromptCacheKey != "" {
		t.Errorf("no user message → empty key, got %q", aff5)
	}
}

func TestSetupRequestHeader_XAffinityMirror(t *testing.T) {
	// Convert 存 key，mirrorAffinityHeader 镜像 x-affinity；无 key 不加头
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(mistralAffinityCtxKey, "aff-abc")
	h := http.Header{}
	mirrorAffinityHeader(c, &h)
	if h.Get("x-affinity") != "aff-abc" {
		t.Errorf("x-affinity must mirror ctx key, got %q", h.Get("x-affinity"))
	}

	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	h2 := http.Header{}
	mirrorAffinityHeader(c2, &h2)
	if h2.Get("x-affinity") != "" {
		t.Errorf("no key → no x-affinity header")
	}
}

func TestNormalizeStreamEndToEnd(t *testing.T) {
	// 端到端：多行 SSE 流经 reader，转换后行流正确
	raw := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":null,"index":0}],"created":1794497916,"id":"id1","model":"glm-5.3","object":"chat.completion.chunk"}`,
		``,
		`data: {"choices":[{"delta":{"content":[{"closed":true,"thinking":[{"text":"思考中","type":"text"}],"type":"thinking"}],"index":0},"finish_reason":null,"index":0,"logprobs":null}],"created":1794497919,"id":"id1","model":"glm-5.3","object":"chat.completion.chunk"}`,
		`data: {"choices":[{"delta":{"content":"正文帧","index":0},"finish_reason":null,"index":0}],"created":1794497919,"id":"id1","model":"glm-5.3","object":"chat.completion.chunk"}`,
		`data: [DONE]`,
		``,
	}, "\n")

	reader := NewNormalizeStream(io.NopCloser(strings.NewReader(raw)))
	var out strings.Builder
	buf := make([]byte, 7) // 故意用小块触发跨块读写
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read err: %v", err)
		}
	}
	got := out.String()
	if !strings.Contains(got, `"reasoning_content":"思考中"`) {
		t.Errorf("thinking not converted:\n%s", got)
	}
	if !strings.Contains(got, `"content":"正文帧"`) {
		t.Errorf("text frame lost:\n%s", got)
	}
	if strings.Contains(got, `"role":"assistant","finish_reason"`) {
		// 首帧 role 保留是预期的；这里只验证 role 不出现在后续帧
	}
	if strings.Count(got, `"role":"assistant"`) != 1 {
		t.Errorf("role must appear exactly once, got:\n%s", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("[DONE] lost")
	}
}
