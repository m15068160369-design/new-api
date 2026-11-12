package mistral

import (
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

var mistralToolCallIdRegexp = regexp.MustCompile("^[a-zA-Z0-9]{9}$")

// [patch lou #15a] 把 assistant 思考内容+正文组装为 Mistral ThinkChunk 数组，
// 形态与上游响应 chunklet 一致（实测 zai-glm-5-3 回传 200）。
func buildAssistantChunklet(reasoning, text string) []any {
	return []any{
		map[string]any{
			"type":     "thinking",
			"closed":   true,
			"thinking": []any{map[string]any{"type": "text", "text": reasoning}},
		},
		map[string]any{
			"type": "text",
			"text": text,
		},
	}
}

// [patch lou #15a] Mistral 的 tool_choice 只认 none/auto/any，
// OpenAI 标准的 "required" 直接透传会被拒，映射为语义最接近的 "any"。
func convertMistralToolChoice(tc any) any {
	if s, ok := tc.(string); ok && s == "required" {
		return "any"
	}
	return tc
}

func requestOpenAI2Mistral(request *dto.GeneralOpenAIRequest) *dto.GeneralOpenAIRequest {
	messages := make([]dto.Message, 0, len(request.Messages))
	idMap := make(map[string]string)
	for _, message := range request.Messages {
		// [patch lou #15a] 在 ParseContent 改写 content 之前提取原始文本与思考：
		// assistant 消息带 reasoning_content 时需回传 Mistral 原生 ThinkChunk 形态
		// （直接透传 reasoning_content 字段会被上游 422 extra_forbidden 拒绝，
		// 实测 chunklet 数组回传 200）。text-only 回传上游也接受，转换按尽力而为。
		originalText, originalIsString := "", false
		if s, ok := message.Content.(string); ok {
			originalText, originalIsString = s, true
		}
		assistantReasoning := ""
		if message.Role == "assistant" {
			assistantReasoning = message.GetReasoningContent()
		}

		// 1. tool_calls.id
		toolCalls := message.ParseToolCalls()
		if toolCalls != nil {
			for i := range toolCalls {
				if !mistralToolCallIdRegexp.MatchString(toolCalls[i].ID) {
					if newId, ok := idMap[toolCalls[i].ID]; ok {
						toolCalls[i].ID = newId
					} else {
						newId, err := common.GenerateRandomCharsKey(9)
						if err == nil {
							idMap[toolCalls[i].ID] = newId
							toolCalls[i].ID = newId
						}
					}
				}
			}
			message.SetToolCalls(toolCalls)
		}

		// 2. tool_call_id
		if message.ToolCallId != "" {
			if newId, ok := idMap[message.ToolCallId]; ok {
				message.ToolCallId = newId
			} else {
				if !mistralToolCallIdRegexp.MatchString(message.ToolCallId) {
					newId, err := common.GenerateRandomCharsKey(9)
					if err == nil {
						idMap[message.ToolCallId] = newId
						message.ToolCallId = newId
					}
				}
			}
		}

		mediaMessages := message.ParseContent()
		if message.Role == "assistant" && message.ToolCalls != nil && message.Content == "" {
			mediaMessages = []dto.MediaContent{}
		}
		for j, mediaMessage := range mediaMessages {
			if mediaMessage.Type == dto.ContentTypeImageURL {
				imageUrl := mediaMessage.GetImageMedia()
				mediaMessage.ImageUrl = imageUrl.Url
				mediaMessages[j] = mediaMessage
			}
		}
		message.SetMediaContent(mediaMessages)
		newMsg := dto.Message{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  message.ToolCalls,
			ToolCallId: message.ToolCallId,
		}
		// [patch lou #15a] 有思考内容的纯文本 assistant 消息 → 转 chunklet 数组回传，
		// 让模型在后续轮次看到自己的推理轨迹（形态与上游响应 chunklet 一致，实测 200）。
		// 覆盖 SetMediaContent 对 Content 的改写；content 为数组/tool_calls 消息不动。
		if message.Role == "assistant" && assistantReasoning != "" && originalIsString &&
			originalText != "" && message.ToolCalls == nil {
			newMsg.Content = buildAssistantChunklet(assistantReasoning, originalText)
		}
		messages = append(messages, newMsg)
	}
	out := &dto.GeneralOpenAIRequest{
		Model:       request.Model,
		Stream:      request.Stream,
		Messages:    messages,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Tools:       request.Tools,
		ToolChoice:  convertMistralToolChoice(request.ToolChoice),
	}
	if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
		maxTokens := request.GetMaxTokens()
		out.MaxTokens = &maxTokens
	}
	return out
}
