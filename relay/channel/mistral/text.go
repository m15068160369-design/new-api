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

// [patch lou #15a] Mistral 的 tool_choice 只认 none/auto/any（required 同样
// 合法，见 openapi ToolChoiceEnum），OpenAI 标准的 "required" 映射为 "any"；
// 对象形态只留 function.name（Mistral FunctionName additionalProperties:false）。
func convertMistralToolChoice(tc any) any {
	if s, ok := tc.(string); ok && s == "required" {
		return "any"
	}
	if m, ok := tc.(map[string]any); ok {
		if fn, ok := m["function"].(map[string]any); ok {
			m["function"] = map[string]any{"name": fn["name"]}
		}
	}
	return tc
}

// [patch lou #19] Mistral 请求包装：embedding 扁平序列化，在 OpenAI 结构上
// 追加 Mistral 专属字段（random_seed）。OpenAI 的 seed 改名转发（float64 取整
// 截断，负值归 0）。
type mistralRequest struct {
	*dto.GeneralOpenAIRequest
	RandomSeed *int64 `json:"random_seed,omitempty"`
}

func requestOpenAI2Mistral(request *dto.GeneralOpenAIRequest) (*mistralRequest, string) {
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
		// [patch lou #19c] chunk 白名单过滤：Mistral ContentChunk oneOf 只认
		// text/image_url/document_url/reference/file/thinking。客户端可能发
		// input_audio/video_url 等 OpenAI/阿里系 chunk，原样序列化会 422；
		// 空 text chunk 的 text 字段 omitempty 丢失会违反 TextChunk required。
		// 只保留 text（补空串保 required）与 image_url（转 url 字符串形态），
		// 其余丢弃；全空时退化为空串 content（所有 role 都接受 string）。
		if message.Role != "assistant" || message.ToolCalls == nil {
			mediaMessages = sanitizeContentChunks(mediaMessages)
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
		// [patch lou #19] 以下字段此前静默丢弃，Mistral schema 原生支持，转发：
		Stop:             request.Stop,
		N:                request.N,
		FrequencyPenalty: request.FrequencyPenalty,
		PresencePenalty:  request.PresencePenalty,
		ResponseFormat:   request.ResponseFormat,
		LogProbs:         request.LogProbs,
		TopLogProbs:     request.TopLogProbs,
		// Seed 不直接放 out（OpenAI 字段名 seed Mistral 不认），包装层改名 random_seed
		Seed: nil,
	}
	// [patch lou #19] 值域合法化（temperature≤1.5 / top_p∈(0,1] / penalties∈[-2,2] / n≥1）
	sanitizeSamplingParams(out)
	// [patch lou #19b] response_format 枚举合法化：非法值归 text
	out.ResponseFormat = sanitizeResponseFormat(out.ResponseFormat)
	if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
		maxTokens := request.GetMaxTokens()
		out.MaxTokens = &maxTokens
	}
	// [patch lou #18] reasoning_effort 合法化（替换 #16 的原样直通）：#13 只修
	// openai 通用路径，Mistral 渠道走本包 ConvertRequest 绕过了它，实测 ch1
	// none/medium 均被上游 400。per-model 合法值集见 reasoning_effort_fix.go，
	// 非法值就近映射（只升不降），合法值透传。
	if request.ReasoningEffort != "" {
		out.ReasoningEffort = normalizeReasoningEffort(request.ReasoningEffort, request.Model)
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
	// [patch lou #19] 包装：embedding 扁平序列化追加 random_seed（OpenAI seed 改名）
	wrapped := &mistralRequest{GeneralOpenAIRequest: out}
	if request.Seed != nil {
		s := int64(*request.Seed)
		if s < 0 {
			s = 0
		}
		wrapped.RandomSeed = &s
	}
	return wrapped, affinityKey
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
