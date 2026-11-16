package mistral

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

// [patch lou #19c] 消息层 chunk 白名单单测。
func TestSanitizeContentChunks(t *testing.T) {
	// input_audio/video_url 被丢弃，text/image_url 保留
	in := []dto.MediaContent{
		{Type: dto.ContentTypeText, Text: "hello"},
		{Type: dto.ContentTypeInputAudio, InputAudio: map[string]any{"data": "x"}},
		{Type: dto.ContentTypeVideoUrl, VideoUrl: map[string]any{"url": "y"}},
		{Type: dto.ContentTypeImageURL, ImageUrl: "https://example.com/a.png"},
	}
	got := sanitizeContentChunks(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (audio/video dropped)", len(got))
	}
	if got[0].Type != "text" || got[0].Text != "hello" {
		t.Errorf("chunk0 = %+v", got[0])
	}
	if got[1].Type != "image_url" {
		t.Errorf("chunk1 = %+v", got[1])
	}
	// 全部丢弃 → 退化空 text
	got2 := sanitizeContentChunks([]dto.MediaContent{
		{Type: dto.ContentTypeInputAudio, InputAudio: "x"},
	})
	if len(got2) != 1 || got2[0].Type != "text" {
		t.Errorf("fallback = %+v, want single empty text", got2)
	}
	// 空 text chunk 保留 type+空串（序列化后 text 字段不丢）
	got3 := sanitizeContentChunks([]dto.MediaContent{{Type: dto.ContentTypeText, Text: ""}})
	if len(got3) != 1 || got3[0].Type != "text" {
		t.Errorf("empty text chunk = %+v", got3)
	}
	// 空列表 → 空 text
	got4 := sanitizeContentChunks(nil)
	if len(got4) != 1 || got4[0].Type != "text" {
		t.Errorf("nil chunks = %+v", got4)
	}
}

// 序列化级断言：含 input_audio 的消息最终 payload 不含 input_audio 键。
func TestRequestOpenAI2Mistral_DropsAudioChunks(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "zai-glm-5-3",
		Messages: []dto.Message{{
			Role: "user",
			Content: json.RawMessage(`[
				{"type":"text","text":"listen"},
				{"type":"input_audio","input_audio":{"data":"abc","format":"wav"}}
			]`),
		}},
	}
	wrapped, _ := requestOpenAI2Mistral(req)
	b, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(b); strings.Contains(bytes, "input_audio") {
		t.Errorf("input_audio leaked into payload: %s", b)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	msgs := m["messages"].([]any)
	content := msgs[0].(map[string]any)["content"]
	arr, ok := content.([]any)
	if !ok || len(arr) != 1 {
		t.Errorf("content = %v, want single text chunk", content)
	}
}

