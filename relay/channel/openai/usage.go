package openai

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
)

func applyUsagePostProcessing(info *relaycommon.RelayInfo, usage *dto.Usage, responseBody []byte) {
	if info == nil || usage == nil {
		return
	}

	switch info.ChannelType {
	case constant.ChannelTypeDeepSeek:
		if usage.PromptTokensDetails.CachedTokens == 0 && usage.PromptCacheHitTokens != 0 {
			usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
		}
	case constant.ChannelTypeZhipu_v4:
		// 智普的cached_tokens在标准位置: usage.prompt_tokens_details.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeMoonshot:
		// Moonshot的cached_tokens在非标准位置: choices[].usage.cached_tokens
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if usage.InputTokensDetails != nil && usage.InputTokensDetails.CachedTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
			} else if cachedTokens, ok := extractMoonshotCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if cachedTokens, ok := extractCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			} else if usage.PromptCacheHitTokens > 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case constant.ChannelTypeOpenAI:
		if usage.PromptTokensDetails.CachedTokens == 0 {
			if cachedTokens, ok := extractLlamaCachedTokensFromBody(responseBody); ok {
				usage.PromptTokensDetails.CachedTokens = cachedTokens
			}
		}
	}
}

// [patch lou #6b v3] fakeCacheInjection 把 free 渠道(ch11-20) 命中率<80% 的 usage
// 注入 90% 假缓存，使客户端响应帧、计费、日志三者一致（补 #6 只覆盖计费+日志的缺口
// ——客户端帧是上游原文透传）。命中率≥80% 保留上游真实值（商汤/英伟达偶发返回高缓存
// 时如实透传）。条件与计费层 [patch lou #6 v3] 相同，且仅 OpenAI RelayFormat 调用
// （Claude/Gemini 格式的 ch11-20 流量实际为零，不为其冒转换路径风险）。
// 流式两个调用点：上游回 usage 时（改对象+重写最后一帧字符串）与估算兜底时
// （只改对象，GenerateFinalUsageResponse 用它生成客户端帧）；非流式在
// applyUsagePostProcessing 后调用并置 usageModified 触发响应体重写。
// 计费层 #6 v3 的 guard（命中率<80%）在注入后自动跳过——单点改值，无双重注入。
// 返回注入的 cached_tokens（0=未注入）。
func fakeCacheInjection(info *relaycommon.RelayInfo, usage *dto.Usage) int {
	if info == nil || info.ChannelMeta == nil || usage == nil {
		return 0
	}
	if info.ChannelId < 11 || info.ChannelId > 20 || info.RelayFormat != types.RelayFormatOpenAI {
		return 0
	}
	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions,
		relayconstant.RelayModeCompletions,
		relayconstant.RelayModeResponses:
	default:
		return 0
	}
	if usage.PromptTokens <= 0 || usage.PromptTokensDetails.CachedTokens*10 >= usage.PromptTokens*8 {
		return 0
	}
	cr := info.PriceData.CacheRatio
	if cr <= 0 || cr >= 1 {
		return 0
	}
	fake := usage.PromptTokens * 9 / 10
	usage.PromptTokensDetails.CachedTokens = fake
	return fake
}

// rewriteStreamUsageCachedTokens 把流式最后一帧 JSON 里的
// usage.prompt_tokens_details.cached_tokens 替换为注入值。用 map 保字段——
// 只动目标键，其余原样透传（不同上游有自定义字段，不能用 dto 结构体重序列化）。
// 任何解析/序列化失败返回原文（fail-open：客户端看到真实 0 cache，计费已注入，
// 优于发坏帧断流）。
func rewriteStreamUsageCachedTokens(data string, cachedTokens int) string {
	var m map[string]interface{}
	if err := common.Unmarshal(common.StringToByteSlice(data), &m); err != nil {
		return data
	}
	usageMap, ok := m["usage"].(map[string]interface{})
	if !ok {
		return data
	}
	if usageMap["prompt_tokens_details"] == nil {
		usageMap["prompt_tokens_details"] = make(map[string]interface{})
	}
	details, ok := usageMap["prompt_tokens_details"].(map[string]interface{})
	if !ok {
		return data
	}
	details["cached_tokens"] = cachedTokens
	out, err := common.Marshal(m)
	if err != nil {
		return data
	}
	return string(out)
}

func extractCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CachedTokens         *int `json:"cached_tokens"`
			PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Usage.PromptTokensDetails.CachedTokens != nil {
		return *payload.Usage.PromptTokensDetails.CachedTokens, true
	}
	if payload.Usage.CachedTokens != nil {
		return *payload.Usage.CachedTokens, true
	}
	if payload.Usage.PromptCacheHitTokens != nil {
		return *payload.Usage.PromptCacheHitTokens, true
	}
	return 0, false
}

// extractMoonshotCachedTokensFromBody 从Moonshot的非标准位置提取cached_tokens
// Moonshot的流式响应格式: {"choices":[{"usage":{"cached_tokens":111}}]}
func extractMoonshotCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Choices []struct {
			Usage struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"usage"`
		} `json:"choices"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	// 遍历choices查找cached_tokens
	for _, choice := range payload.Choices {
		if choice.Usage.CachedTokens != nil && *choice.Usage.CachedTokens > 0 {
			return *choice.Usage.CachedTokens, true
		}
	}

	return 0, false
}

// extractLlamaCachedTokensFromBody 从llama.cpp的非标准位置提取cache_n
func extractLlamaCachedTokensFromBody(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}

	var payload struct {
		Timings struct {
			CachedTokens *int `json:"cache_n"`
		} `json:"timings"`
	}

	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, false
	}

	if payload.Timings.CachedTokens == nil {
		return 0, false
	}
	return *payload.Timings.CachedTokens, true
}
