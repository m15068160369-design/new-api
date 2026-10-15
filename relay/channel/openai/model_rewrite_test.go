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
