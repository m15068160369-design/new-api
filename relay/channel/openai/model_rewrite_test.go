package openai

// [patch lou #11] 响应 model 字段重写单测：覆盖重写正确性、内容字符串里的
// "model" 文本不受影响、无 model 字段/解析失败 fail-open 退化透传、
// responses 格式两层 model 都重写。
import (
	"encoding/json"
	"testing"
)

func TestRewriteStreamModelField_Basic(t *testing.T) {
	frame := `{"id":"chatcmpl-x","object":"chat.completion.chunk","created":123,"model":"zai-org/GLM-5.2","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
	out, ok := rewriteStreamModelField(frame, "glm")
	if !ok {
		t.Fatal("expected ok=true")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("rewritten frame is not valid JSON: %v", err)
	}
	if m["model"] != "glm" {
		t.Fatalf("model not rewritten: %v", m["model"])
	}
	if m["id"] != "chatcmpl-x" {
		t.Fatalf("other top-level field lost: %v", m["id"])
	}
	choices, ok := m["choices"].([]interface{})
	if !ok || len(choices) != 1 {
		t.Fatalf("choices lost: %v", m["choices"])
	}
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "hi" {
		t.Fatalf("delta content lost: %v", delta)
	}
}

func TestRewriteStreamModelField_ContentNotTouched(t *testing.T) {
	// 内容字符串里出现 "model" 文本——只重写顶层键，内容不动
	frame := `{"model":"zai-org/GLM-5.2","choices":[{"index":0,"delta":{"content":"the \"model\" field in JSON is \"model\": true"},"finish_reason":null}]}`
	out, ok := rewriteStreamModelField(frame, "glm")
	if !ok {
		t.Fatal("expected ok=true")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("rewritten frame is not valid JSON: %v", err)
	}
	if m["model"] != "glm" {
		t.Fatalf("model not rewritten: %v", m["model"])
	}
	content := m["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["content"]
	if content != `the "model" field in JSON is "model": true` {
		t.Fatalf("content modified: %v", content)
	}
}

func TestRewriteStreamModelField_NoModelField(t *testing.T) {
	frame := `{"type":"ping","choices":[]}`
	out, ok := rewriteStreamModelField(frame, "glm")
	if ok {
		t.Fatalf("expected ok=false for frame without model field, got %s", out)
	}
	if out != frame {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

func TestRewriteStreamModelField_InvalidJSON(t *testing.T) {
	frame := `not json at all`
	out, ok := rewriteStreamModelField(frame, "glm")
	if ok {
		t.Fatalf("expected ok=false for invalid JSON, got %s", out)
	}
	if out != frame {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

func TestRewriteResponsesModelField_TwoLevels(t *testing.T) {
	frame := `{"type":"response.completed","sequence_number":42,"response":{"id":"resp_1","model":"zai-org/GLM-5.2","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`
	out, ok := rewriteResponsesModelField([]byte(frame), "glm")
	if !ok {
		t.Fatal("expected ok=true")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("rewritten frame is not valid JSON: %v", err)
	}
	if m["type"] != "response.completed" {
		t.Fatalf("type lost: %v", m["type"])
	}
	resp := m["response"].(map[string]interface{})
	if resp["model"] != "glm" {
		t.Fatalf("response.model not rewritten: %v", resp["model"])
	}
	usage := resp["usage"].(map[string]interface{})
	if usage["input_tokens"].(float64) != 10 {
		t.Fatalf("usage lost: %v", usage)
	}
}

func TestRewriteResponsesModelField_TopLevel(t *testing.T) {
	// 非流式 responses 响应体：顶层 model
	frame := `{"id":"resp_1","object":"response","model":"zai-org/GLM-5.2","status":"completed"}`
	out, ok := rewriteResponsesModelField([]byte(frame), "glm")
	if !ok {
		t.Fatal("expected ok=true")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("rewritten body is not valid JSON: %v", err)
	}
	if m["model"] != "glm" {
		t.Fatalf("model not rewritten: %v", m["model"])
	}
	if m["id"] != "resp_1" {
		t.Fatalf("id lost: %v", m["id"])
	}
}

func TestRewriteResponsesModelField_NoModel(t *testing.T) {
	frame := `{"type":"response.output_text.delta","delta":"hi"}`
	out, ok := rewriteResponsesModelField([]byte(frame), "glm")
	if ok {
		t.Fatalf("expected ok=false for frame without model, got %s", out)
	}
	if string(out) != frame {
		t.Fatalf("expected passthrough, got %s", out)
	}
}

func TestCleanUpstreamLeakage_ProviderMetadataAndCost(t *testing.T) {
	// 实测样本（魔搭 ch11 最后帧）：provider_metadata 含上游路由信息，
	// usage 含 cost/market_cost/is_byok 成本字段
	frame := `{"id":"gen_x","object":"chat.completion.chunk","created":1792083797,"model":"vmc/glm-5p3-contributor-fallbacks","system_fingerprint":"fp_x","usage":{"prompt_tokens":16,"completion_tokens":30,"total_tokens":46,"cost":0.000143,"market_cost":0.000143,"is_byok":false,"cost_details":null},"choices":[{"index":0,"delta":{"role":null,"provider_metadata":{"gateway":{"routing":{"canonicalSlug":"private/glm-5p3-contributor","finalProvider":"openai-compatible-private"}}}},"finish_reason":"length"}]}`
	out, ok := rewriteStreamModelField(frame, "glm-5.3")
	if !ok {
		t.Fatal("expected ok=true")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("rewritten frame is not valid JSON: %v", err)
	}
	if m["model"] != "glm-5.3" {
		t.Fatalf("model not rewritten: %v", m["model"])
	}
	choice := m["choices"].([]interface{})[0].(map[string]interface{})
	delta := choice["delta"].(map[string]interface{})
	if _, ok := delta["provider_metadata"]; ok {
		t.Fatal("delta.provider_metadata not removed")
	}
	usage := m["usage"].(map[string]interface{})
	for _, k := range []string{"cost", "market_cost", "is_byok", "cost_details"} {
		if _, ok := usage[k]; ok {
			t.Fatalf("usage.%s not removed", k)
		}
	}
	if usage["prompt_tokens"].(float64) != 16 {
		t.Fatalf("usage token fields lost: %v", usage)
	}
	// 帧尾判断字段保留
	if choice["finish_reason"] != "length" {
		t.Fatalf("finish_reason lost: %v", choice)
	}
}

func TestCleanUpstreamLeakage_MessageProviderMetadata(t *testing.T) {
	// 非流式形态：choices[].message.provider_metadata（实测魔搭非流式帧 delta+message 并存）
	frame := `{"model":"zai-org/GLM-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"hi","provider_metadata":{"x":1}},"finish_reason":null}],"usage":{"total_tokens":5,"cost":0.001}}`
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(frame), &m); err != nil {
		t.Fatal(err)
	}
	if !cleanUpstreamLeakage(m, "glm-5.3") {
		t.Fatal("expected changed=true")
	}
	msg := m["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	if _, ok := msg["provider_metadata"]; ok {
		t.Fatal("message.provider_metadata not removed")
	}
	if msg["content"] != "hi" {
		t.Fatalf("message content lost: %v", msg)
	}
	if _, ok := m["usage"].(map[string]interface{})["cost"]; ok {
		t.Fatal("usage.cost not removed")
	}
}
