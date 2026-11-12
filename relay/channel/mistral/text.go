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

func requestOpenAI2Mistral(request *dto.GeneralOpenAIRequest) (*dto.GeneralOpenAIRequest, string) {
	// [patch lou #16] 返回第二值：本请求的缓存亲和 key（prompt_cache_key / 派生值），
	// 供 SetupRequestHeader 镜像进 x-affinity。
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
		// [patch lou #16] Mistral 只认 system/user/assistant/tool 四种 role（实测 422
		// `Input tag 'developer' ... does not match any of the expected tags`，
		// linux.do/t/topic/3149127 坑1）。Codex/CPA 客户端会发 developer，统一降为 system。
		role := message.Role
		if role == "developer" {
			role = "system"
		}
		newMsg := dto.Message{
			Role:       role,
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
	// [patch lou #16] reasoning_effort 透传：此前白名单静默丢弃，楼补丁 #13 在网关层
	// 纠正过的合法值（zai-glm-5-3 认 max/high/low）根本到不了上游。现在保持客户端/网关
	// 注入值原样上行。注意：未来渠道若加 mistral 自家非思考模型（如 mistral-large 会报
	// reasoning_effort is not enabled），需按模型评估是否透传。
	if request.ReasoningEffort != "" {
		out.ReasoningEffort = request.ReasoningEffort
	}
	// [patch lou #16] 缓存亲和 key：客户端给了 prompt_cache_key（CPA 的
	// support-prompt-cache-key 开关即发此字段）则原样透传；否则从对话根
	// （首条 system/developer 指令 + 首条 user 消息）派生稳定 key，让同一会话的
	// 后续轮次与压缩重放命中同一缓存节点。key 同时镜像进 x-affinity header
	// （vibe 端点习惯，kaecho/mistral 实践）。
	affinityKey := request.PromptCacheKey
	if affinityKey == "" {
		affinityKey = deriveConversationAffinityKey(request.Messages)
	}
	out.PromptCacheKey = affinityKey
	return out, affinityKey
}

// [patch lou #16b] 从对话根派生稳定的缓存亲和 key：取首条 user 消息
// 的前 64 个字符（rune）明文。会话演进中首条 user 不变，故每轮 key 相同、
// 同一缓存节点（Mistral 官方机制：prompt_cache_key 提高缓存命中路由概率）。
// 楼先生拍板由 SHA-256 指纹改为明文前缀：效果等价，中转站场景零开销优先。
// 按字符截断避免中文拦腰断字节；短首消息（<64 字符）区分度略降，
// 撞 key 只影响路由不影响正确性。
func deriveConversationAffinityKey(messages []dto.Message) string {
	root := ""
	for _, m := range messages {
		if m.Role == "user" {
			root = m.StringContent()
			break
		}
	}
	if root == "" {
		return ""
	}
	runes := []rune(root)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}
