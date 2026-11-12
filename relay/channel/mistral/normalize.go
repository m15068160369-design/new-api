package mistral

// [patch lou #15a/#15b] Mistral 响应体格式归一化。
//
// 背景：Mistral La Plateforme 的 /v1/chat/completions 对思考类模型（zai-glm-5-3 等）
// 返回 chunklet 数组形态的 delta.content / message.content：
//
//	[{"closed":true,"thinking":[{"text":"...","type":"text"}],"type":"thinking"},
//	 {"text":"...","type":"text"}]
//
// 标准 OpenAI Chat Completions 协议要求 content 为字符串、思考内容放
// reasoning_content 字段。严格校验的客户端（如 Cherry Studio 内置 AI SDK）在
// content 数组帧上直接抛 AI_TypeValidationError，流中断；New API 自身的
// processTokenData 也解析不出文本（计费估算落空）。
//
// 本包在 mistral 渠道（type 42）专属 adaptor 内将响应归一为标准 OpenAI 形态：
//   - 流式：逐行 SSE，data 帧里 content 数组拆块，thinking→reasoning_content，
//     text→content；未变化的帧字节级原样透传（fail-open）
//   - 非流式：message.content 数组同规则拆块
//   - 清洗 Mistral 私有字段：delta.index、choice.response_error、message.prefix、
//     usage 顶层 input_tokens/output_tokens/input_tokens_details/claude_cache_*、
//     usage details 内 text/audio/image_tokens 零值键
//   - 流式 role 只在首帧保留（官方行为），后续帧剔除
//   - finish_reason 非标枚举映射：model_length→length、error→stop
//   - tool_calls 兼容：空 name 剔除、id=="null" 剔除、arguments 对象转字符串
//     （非流式另剔除 tool_calls 的 index 字段）
//
// 爆炸半径：仅渠道类型 42（mistral adaptor 唯一路由），不动 openai 共享代码。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// finishReasonMap 把 Mistral 非标 finish_reason 映射为 OpenAI 标准枚举。
var finishReasonMap = map[string]string{
	"model_length": "length",
	"error":        "stop",
}

// script 非流式 usage 顶层私有键（实测 zai-glm-5-3 非流式响应携带）。
var usageTopPrivateKeys = []string{
	"input_tokens", "output_tokens", "input_tokens_details",
	"claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens",
}

// usage details 内零值噪声键。
var usageDetailPrivateKeys = []string{"text_tokens", "audio_tokens", "image_tokens"}

// chunkletBlocks 把 content 数组拆为思考文本与正文文本。
// thinking 字段兼容 string、[{text:...}] 两种形态；未知块若有 text 字段并入正文。
func chunkletBlocks(content []interface{}) (thinking, text string) {
	var thinkSB, textSB strings.Builder
	for _, blockAny := range content {
		block, ok := blockAny.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "thinking":
			switch inner := block["thinking"].(type) {
			case string:
				thinkSB.WriteString(inner)
			case []interface{}:
				for _, itemAny := range inner {
					switch item := itemAny.(type) {
					case map[string]interface{}:
						if s, ok := item["text"].(string); ok {
							thinkSB.WriteString(s)
						}
					case string:
						thinkSB.WriteString(item)
					}
				}
			}
		case "text":
			if s, ok := block["text"].(string); ok {
				textSB.WriteString(s)
			}
		default:
			// 未知块类型：带 text 字段则并入正文，否则丢弃
			if s, ok := block["text"].(string); ok {
				textSB.WriteString(s)
			}
		}
	}
	return thinkSB.String(), textSB.String()
}

// normalizeToolCalls 清洗 tool_calls 数组。isStream=true 时保留增量关联的 index。
func normalizeToolCalls(toolCalls []interface{}, isStream bool) bool {
	changed := false
	for _, tcAny := range toolCalls {
		tc, ok := tcAny.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := tc["id"].(string); ok && id == "null" {
			delete(tc, "id")
			changed = true
		}
		if !isStream {
			if _, has := tc["index"]; has {
				delete(tc, "index")
				changed = true
			}
		}
		if fn, ok := tc["function"].(map[string]interface{}); ok {
			if name, ok := fn["name"].(string); ok && name == "" {
				delete(fn, "name")
				changed = true
			}
			switch args := fn["arguments"].(type) {
			case map[string]interface{}:
				if b, err := json.Marshal(args); err == nil {
					fn["arguments"] = string(b)
					changed = true
				}
			case []interface{}:
				if b, err := json.Marshal(args); err == nil {
					fn["arguments"] = string(b)
					changed = true
				}
			}
		}
	}
	return changed
}

// cleanUsageMap 清洗 usage 对象里的 Mistral 私有键。
func cleanUsageMap(usage map[string]interface{}) bool {
	changed := false
	for _, k := range usageTopPrivateKeys {
		if _, has := usage[k]; has {
			delete(usage, k)
			changed = true
		}
	}
	for _, detailKey := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		if detail, ok := usage[detailKey].(map[string]interface{}); ok {
			for _, k := range usageDetailPrivateKeys {
				if _, has := detail[k]; has {
					delete(detail, k)
					changed = true
				}
			}
		}
	}
	return changed
}

// normalizeChoice 处理单个 choice（流式 delta 与非流式 message 共用）。
func normalizeChoice(choice map[string]interface{}, roleSeen *bool, isStream bool) bool {
	changed := false
	if _, has := choice["response_error"]; has {
		delete(choice, "response_error")
		changed = true
	}
	if finish, ok := choice["finish_reason"].(string); ok {
		if mapped, has := finishReasonMap[finish]; has {
			choice["finish_reason"] = mapped
			changed = true
		}
	}

	if isStream {
		if delta, ok := choice["delta"].(map[string]interface{}); ok {
			changed = normalizeDelta(delta, roleSeen) || changed
		}
	} else {
		if msg, ok := choice["message"].(map[string]interface{}); ok {
			changed = normalizeMessage(msg) || changed
		}
	}
	return changed
}

// Carrier 标签不会被使用，移除此行（保持 gofmt/编译干净）。

func normalizeDelta(delta map[string]interface{}, roleSeen *bool) bool {
	changed := false
	if contentArr, ok := delta["content"].([]interface{}); ok {
		thinking, text := chunkletBlocks(contentArr)
		delete(delta, "content")
		if thinking != "" {
			delta["reasoning_content"] = thinking
		}
		if text != "" {
			delta["content"] = text
		}
		changed = true
	}
	if _, has := delta["index"]; has {
		delete(delta, "index")
		changed = true
	}
	if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		changed = normalizeToolCalls(toolCalls, true) || changed
	}
	if role, ok := delta["role"].(string); ok && role != "" {
		if roleSeen != nil {
			if *roleSeen {
				delete(delta, "role")
			} else {
				*roleSeen = true
			}
		}
	}
	return changed
}

func normalizeMessage(msg map[string]interface{}) bool {
	changed := false
	if contentArr, ok := msg["content"].([]interface{}); ok {
		thinking, text := chunkletBlocks(contentArr)
		if thinking != "" {
			msg["reasoning_content"] = thinking
		}
		msg["content"] = text
		changed = true
	}
	if v, has := msg["tool_calls"]; has {
		if v == nil {
			delete(msg, "tool_calls")
			changed = true
		} else if tcs, ok := v.([]interface{}); ok {
			if len(tcs) == 0 {
				delete(msg, "tool_calls")
				changed = true
			} else {
				changed = normalizeToolCalls(tcs, false) || changed
			}
		}
	}
	if _, has := msg["prefix"]; has {
		delete(msg, "prefix")
		changed = true
	}
	return changed
}

// normalizeChunkMap 处理一帧 chat.completion.chunk 或一个非流式响应顶层 map。
// 返回是否有改动；无改动时调用方应原样输出原字节。
func normalizeChunkMap(m map[string]interface{}, roleSeen *bool, isStream bool) bool {
	changed := false
	if choices, ok := m["choices"].([]interface{}); ok {
		for _, choiceAny := range choices {
			if choice, ok := choiceAny.(map[string]interface{}); ok {
				changed = normalizeChoice(choice, roleSeen, isStream) || changed
			}
		}
	}
	if usage, ok := m["usage"].(map[string]interface{}); ok {
		changed = cleanUsageMap(usage) || changed
	}
	// created 补齐：仅对带 choices 的正常 chunk/body 生效，错误体（如 {detail:[...]})不碰
	if _, has := m["created"]; !has {
		if _, hasChoices := m["choices"]; hasChoices {
			m["created"] = time.Now().Unix()
			changed = true
		}
	}
	return changed
}

// NormalizeChatCompletionBody 归一化非流式响应体；解析失败原样返回（fail-open）。
func NormalizeChatCompletionBody(body []byte) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if m == nil {
		return body
	}
	if !normalizeChunkMap(m, nil, false) {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// normalizeSSELine 处理一行 SSE 文本，返回处理后文本（不含换行符）。
// 未触动的行字节级原样返回。
func normalizeSSELine(line string, roleSeen *bool) string {
	trimmed := strings.TrimRight(line, "\r")
	if !strings.HasPrefix(trimmed, "data:") {
		return trimmed
	}
	payload := strings.TrimPrefix(trimmed, "data:")
	payload = strings.TrimLeft(payload, " ")
	if payload == "[DONE]" {
		return trimmed
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &m); err != nil || m == nil {
		return trimmed
	}
	if !normalizeChunkMap(m, roleSeen, true) {
		return trimmed
	}
	out, err := json.Marshal(m)
	if err != nil {
		return trimmed
	}
	return "data: " + string(out)
}

// normalizeStreamReader 把上游 SSE 流逐行归一化后交给下游 handler。
// 零变化的帧按原字节透传，保证未知新形态度与上游一致（fail-open）。
type normalizeStreamReader struct {
	scanner  *bufio.Scanner
	buf      bytes.Buffer
	roleSeen bool
	src      io.ReadCloser
	eof      bool
}

// NewNormalizeStream 包装上游响应体。
func NewNormalizeStream(src io.ReadCloser) *normalizeStreamReader {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &normalizeStreamReader{scanner: sc, src: src}
}

func (n *normalizeStreamReader) Read(p []byte) (int, error) {
	for n.buf.Len() == 0 {
		if n.eof {
			return 0, io.EOF
		}
		if !n.scanner.Scan() {
			n.eof = true
			if err := n.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		line := n.scanner.Text()
		n.buf.WriteString(normalizeSSELine(line, &n.roleSeen))
		n.buf.WriteByte('\n')
	}
	return n.buf.Read(p)
}

func (n *normalizeStreamReader) Close() error {
	return n.src.Close()
}
