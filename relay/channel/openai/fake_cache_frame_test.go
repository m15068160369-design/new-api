package openai

// [patch lou #6b v6] fakeCacheInjection + rewriteStreamUsageCachedTokens 的测试。
// 覆盖：ch11-20 OpenAI 流式注入、范围守卫（ch10/ch21 边界外、ch20 边界内）、
// RelayMode 守卫、RelayFormat 守卫、上游真缓存<80% 覆盖为双参 a·p−D 值(v6: a=0.9988/
// D=1003，小 p 低命中、p≤~1003 不注入保留真实)、真缓存≥80% 保留、CacheRatio 边界、
// 双参接线、小输入保留真实报告、帧重写（含上游缺 prompt_tokens_details 字段时补造）、
// 帧重写 fail-open（非 JSON 原样返回）。双参边界表由 common/fake_cache_curve_test.go
// 覆盖。

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/require"
)

func make6bInfo(channelId, relayMode int, relayFormat types.RelayFormat, cacheRatio float64) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayFormat:     relayFormat,
		OriginModelName: "glm-5.3",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelId},
		RelayMode:       relayMode,
		PriceData: types.PriceData{
			CacheRatio:     cacheRatio,
			ModelRatio:     1,
			CompletionRatio: 3.5,
		},
	}
}

func TestFakeCacheInjectionCh11OpenAI(t *testing.T) {
	info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 2000}
	fake := fakeCacheInjection(info, usage)
	require.Equal(t, 994, fake) // p=2000 → a·p−D = 994（49.7%，小输入低命中）
	require.Equal(t, 994, usage.PromptTokensDetails.CachedTokens)
}

// [patch lou #6b v6] 双参 a·p−D 接线验证（p≤~1003 值≤0 不注入；边界表由
// common/fake_cache_curve_test.go 覆盖）
func TestFakeCacheInjectionByPromptSize(t *testing.T) {
	cases := []struct {
		p    int
		want int
	}{
		{1000, 0}, {2000, 994}, {5000, 3991}, {10000, 8985},
		{20000, 18973}, {50000, 48937}, {100000, 98877}, {250000, 248697},
	}
	for _, c := range cases {
		info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
		usage := &dto.Usage{PromptTokens: c.p}
		require.Equal(t, c.want, fakeCacheInjection(info, usage), "p=%d", c.p)
	}
}

func TestFakeCacheInjectionCh20RangeBoundaries(t *testing.T) {
	// ch20 在范围(11-20)内 → 注入；ch10/ch21 在范围外 → 不注入
	for _, ch := range []int{10, 21} {
		info := make6bInfo(ch, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
		usage := &dto.Usage{PromptTokens: 1000}
		require.Equal(t, 0, fakeCacheInjection(info, usage), "ch%d 应被范围守卫拦截", ch)
		require.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
	}
	info := make6bInfo(20, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 2000}
	require.Equal(t, 994, fakeCacheInjection(info, usage)) // p=2000 → 994
	require.Equal(t, 994, usage.PromptTokensDetails.CachedTokens)
}

func TestFakeCacheInjectionGuardEmbedding(t *testing.T) {
	info := make6bInfo(11, relayconstant.RelayModeEmbeddings, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 1000}
	require.Equal(t, 0, fakeCacheInjection(info, usage))
}

func TestFakeCacheInjectionGuardClaudeFormat(t *testing.T) {
	info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatClaude, 0.25)
	usage := &dto.Usage{PromptTokens: 1000}
	require.Equal(t, 0, fakeCacheInjection(info, usage))
}

func TestFakeCacheInjectionLowCacheOverridden(t *testing.T) {
	// 上游真实缓存命中率 30% (<80%) → 覆盖为双参值（p=2000 → 994）
	info := make6bInfo(12, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 2000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 600}}
	fake := fakeCacheInjection(info, usage)
	require.Equal(t, 994, fake)
	require.Equal(t, 994, usage.PromptTokensDetails.CachedTokens)
}

func TestFakeCacheInjectionSmallPromptKeepsReal(t *testing.T) {
	// v6：p≤~1003 双参值≤0 → 不注入，保留上游真实报告（帧/计费/日志一致的关键语义）
	info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 500}}
	require.Equal(t, 0, fakeCacheInjection(info, usage))
	require.Equal(t, 500, usage.PromptTokensDetails.CachedTokens) // 真实 500 原样保留
}

func TestFakeCacheInjectionHighCacheUntouched(t *testing.T) {
	// 上游真实缓存命中率 ≥80% → 保留真实值不注入
	info := make6bInfo(12, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	usage := &dto.Usage{PromptTokens: 1000, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 800}}
	require.Equal(t, 0, fakeCacheInjection(info, usage))
	require.Equal(t, 800, usage.PromptTokensDetails.CachedTokens)
}

func TestFakeCacheInjectionRatioBoundaries(t *testing.T) {
	for _, cr := range []float64{0, 1.0, 1.5} {
		info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, cr)
		usage := &dto.Usage{PromptTokens: 1000}
		require.Equal(t, 0, fakeCacheInjection(info, usage), "ratio=%v", cr)
	}
}

func TestFakeCacheInjectionNilMeta(t *testing.T) {
	info := make6bInfo(11, relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, 0.25)
	info.ChannelMeta = nil
	usage := &dto.Usage{PromptTokens: 1000}
	require.Equal(t, 0, fakeCacheInjection(info, usage))
}

func TestRewriteStreamUsageCachedTokens(t *testing.T) {
	frame := `{"id":"x","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":1000}}}`
	out := rewriteStreamUsageCachedTokens(frame, 900)
	require.Contains(t, out, `"cached_tokens":900`)
	require.Contains(t, out, `"text_tokens":1000`)   // 其他字段保住
	require.Contains(t, out, `"prompt_tokens":1000`) // 不动顶层
}

func TestRewriteStreamUsageCreatesDetails(t *testing.T) {
	// 上游没回 prompt_tokens_details 字段时补造
	frame := `{"id":"x","usage":{"prompt_tokens":1000,"completion_tokens":5}}`
	out := rewriteStreamUsageCachedTokens(frame, 900)
	require.Contains(t, out, `"cached_tokens":900`)
	require.Contains(t, out, `"prompt_tokens":1000`)
}

func TestRewriteStreamUsageFailOpen(t *testing.T) {
	garbage := "not-json{{{"
	require.Equal(t, garbage, rewriteStreamUsageCachedTokens(garbage, 900))
	// 无 usage 键也原样返回
	noUsage := `{"id":"x"}`
	require.Equal(t, noUsage, rewriteStreamUsageCachedTokens(noUsage, 900))
}
