package mistral

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

// [patch lou #19] 兼容层单测：值域 clamp、seed→random_seed 改名、
// tool_choice 对象清洗、字段转发完整性。
func TestClampFloat(t *testing.T) {
	nilPtr := (*float64)(nil)
	if got := clampFloat(nilPtr, 0, 1.5); got != nil {
		t.Errorf("clampFloat(nil) = %v, want nil", got)
	}
	v := 2.0
	if got := clampFloat(&v, 0, 1.5); *got != 1.5 {
		t.Errorf("clampFloat(2.0) = %v, want 1.5", *got)
	}
	v = -0.5
	if got := clampFloat(&v, 0, 1.5); *got != 0 {
		t.Errorf("clampFloat(-0.5) = %v, want 0", *got)
	}
	v = 0.7
	if got := clampFloat(&v, 0, 1.5); *got != 0.7 {
		t.Errorf("clampFloat(0.7) = %v, want 0.7 (界内零改动)", *got)
	}
}

func TestSanitizeSamplingParams(t *testing.T) {
	temp := 2.0
	topP := 0.0
	fp := 3.0
	pp := -3.0
	n := 0
	req := &dto.GeneralOpenAIRequest{
		Temperature:      &temp,
		TopP:             &topP,
		FrequencyPenalty: &fp,
		PresencePenalty:  &pp,
		N:                &n,
	}
	sanitizeSamplingParams(req)
	if *req.Temperature != 1.5 {
		t.Errorf("temperature = %v, want 1.5", *req.Temperature)
	}
	if *req.TopP <= 0 || *req.TopP > 1 {
		t.Errorf("top_p = %v, want in (0,1]", *req.TopP)
	}
	if *req.FrequencyPenalty != 2 {
		t.Errorf("frequency_penalty = %v, want 2", *req.FrequencyPenalty)
	}
	if *req.PresencePenalty != -2 {
		t.Errorf("presence_penalty = %v, want -2", *req.PresencePenalty)
	}
	if *req.N != 1 {
		t.Errorf("n = %v, want 1", *req.N)
	}
}

func TestConvertMistralToolChoiceObject(t *testing.T) {
	// required → any（旧行为保持）
	if got := convertMistralToolChoice("required"); got != "any" {
		t.Errorf("required → %v, want any", got)
	}
	// 对象形态只留 name
	tc := map[string]any{
		"type": "function",
		"function": map[string]any{"name": "get_weather", "description": "extra"},
	}
	got := convertMistralToolChoice(tc).(map[string]any)
	fn := got["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function.name = %v", fn["name"])
	}
	if _, has := fn["description"]; has {
		t.Errorf("function.description should be stripped")
	}
	// 字符串合法值不动
	if got := convertMistralToolChoice("auto"); got != "auto" {
		t.Errorf("auto → %v", got)
	}
}

func TestSanitizeResponseFormat(t *testing.T) {
	// nil 不动
	if got := sanitizeResponseFormat(nil); got != nil {
		t.Errorf("nil → %v, want nil", got)
	}
	// 合法值透传（含 json_schema 保留 JsonSchema 结构）
	js := &dto.ResponseFormat{Type: "json_schema", JsonSchema: json.RawMessage(`{"name":"x"}`)}
	if got := sanitizeResponseFormat(js); got != js {
		t.Errorf("json_schema should pass through unchanged")
	}
	// 非法值归 text
	for _, bad := range []string{"grammar", "json", "", "JSON"} {
		if got := sanitizeResponseFormat(&dto.ResponseFormat{Type: bad}); got.Type != "text" {
			t.Errorf("response_format(%q).Type = %q, want text", bad, got.Type)
		}
	}
}

func TestMistralRequestSerialization(t *testing.T) {
	// seed 改名 random_seed + 无 seed 字段泄漏
	seed := 42.0
	req := &dto.GeneralOpenAIRequest{
		Model:    "zai-glm-5-3",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
		Seed:     &seed,
	}
	wrapped, _ := requestOpenAI2Mistral(req)
	b, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, has := m["seed"]; has {
		t.Errorf("seed field leaked: %s", b)
	}
	if m["random_seed"] != float64(42) {
		t.Errorf("random_seed = %v, want 42", m["random_seed"])
	}
	// 无 seed 时不发 random_seed
	req2 := &dto.GeneralOpenAIRequest{
		Model:    "zai-glm-5-3",
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
	}
	w2, _ := requestOpenAI2Mistral(req2)
	b2, _ := json.Marshal(w2)
	var m2 map[string]any
	json.Unmarshal(b2, &m2)
	if _, has := m2["random_seed"]; has {
		t.Errorf("random_seed should be omitted when unset")
	}
	// 转发字段存在性：stop / response_format
	stop := []string{"END"}
	req3 := &dto.GeneralOpenAIRequest{
		Model:         "zai-glm-5-3",
		Messages:      []dto.Message{{Role: "user", Content: "hi"}},
		Stop:          stop,
		ResponseFormat: &dto.ResponseFormat{Type: "json_object"},
	}
	w3, _ := requestOpenAI2Mistral(req3)
	b3, _ := json.Marshal(w3)
	var m3 map[string]any
	json.Unmarshal(b3, &m3)
	if _, has := m3["stop"]; !has {
		t.Errorf("stop missing in payload: %s", b3)
	}
	rf, _ := m3["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_object" {
		t.Errorf("response_format missing/wrong: %s", b3)
	}
}
