package mistral

import (
	"crypto/sha256"
	"fmt"
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
	// [patch lou #16d] 缓存亲和 key 升级为 UUID 形状（对齐官方 mistral-vibe
	// generate_session_id 的 xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx 形状——社区
	// 实测非 UUID 形状的 x-affinity 值可能被上游静默忽略，20261116 源码核对
	// 官方客户端全仓库不发 prompt_cache_key（adapters/mistral.py 的 kwargs 里
	// 无此字段），楼拍板我们也不发 body 字段，只走 x-affinity header 通道。
	// 种子仍然取首条 user 前 64 字符（#16b，会话内稳定），SHA-256 派生确定性
	// UUID：同会话同 UUID、异会话异 UUID，且天然 ASCII（hex），无需 base64。
	// 客户端显式传 prompt_cache_key 的也纳入种子（尊重其分组意图）但不透传。
	seed := request.PromptCacheKey
	if seed == "" {
		seed = deriveConversationAffinityKey(request.Messages)
	}
	affinityKey := affinityUUID(seed)
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

// [patch lou #16e] 从对话根派生稳定的缓存亲和种子：首条 user 消息
// 的前 256 个字节（字节截断，非 rune——[]rune 全量转换随消息长度线性涨，
// 实测 12KB 首消息 51.5µs/16.6KB 分配 vs 字节截断 1.3ns/零分配，楼要求
// CPU 最低 20261117）。截断可能切在多字节字符中间——种子只喂哈希，确定性
// 不受损（同消息字节必同）；撞 key 只影响路由不影响正确性。
// #16d 起本值只作为种子，进 affinityUUID 哈希成 UUID 形状再上 header。
func deriveConversationAffinityKey(messages []dto.Message) string {
	root := ""
	for _, m := range messages {
		if m.Role == "user" {
			root = m.StringContent()
			break
		}
	}
	if len(root) > 256 {
		root = root[:256]
	}
	return root
}

// [patch lou #16d] 种子 → 确定性 UUID（v4 形状）：SHA-256 取前 16 字节，
// 强制 version=4（第 7 字节高 4 位）与 RFC 4122 variant（第 9 字节高 2 位），
// 格式化为官方 mistral-vibe generate_session_id 同款形状。确定性保证同种子
// 同 UUID（会话内稳定、重启不变）；hex 天然 ASCII，无需 base64 编码。
func affinityUUID(seed string) string {
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
