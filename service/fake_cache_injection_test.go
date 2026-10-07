package service

// [patch lou #6 v3] 假缓存注入的测试矩阵。
// 覆盖 calculateTextQuotaSummary 中 ch11-20 假缓存注入的每个 guard 分支：
// 渠道范围（ch10/ch21 边界外、ch20 边界内）、RelayMode、上游缓存命中率
// <80% 覆盖为 90%、≥80% 保留真实值、CacheRatio 边界（1/0）、零 prompt。
// 预期 Quota 推导（非 Claude 语义，OpenAI 渠道）：
//   promptQuota = (prompt - cache) + cache*CacheRatio
//   quota = promptQuota + completion*CompletionRatio，全乘 ModelRatio*GroupRatio(=1)
// 注意：0 CacheRatio 时上游 1000 prompt 无折扣 → 1000 + 350 = 1350。

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func makeFakeCacheTestSetup(prompt, cached int, cacheRatio float64, channelId, relayMode int) (*gin.Context, *relaycommon.RelayInfo, *dto.Usage) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "glm-5.3",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelId},
		RelayMode:       relayMode,
		PriceData: types.PriceData{
			ModelRatio:      1,
			CompletionRatio: 3.5,
			CacheRatio:      cacheRatio,
			GroupRatioInfo:  types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:        prompt,
		CompletionTokens:    100,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: cached},
	}

	return ctx, relayInfo, usage
}

func TestFakeCacheInjectionCh11Chat(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0.25, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// fake cache = 900; (1000-900) + 900*0.25 = 325; + 100*3.5 = 675
	require.Equal(t, 900, summary.CacheTokens)
	require.Equal(t, 675, summary.Quota)
}

func TestFakeCacheInjectionCh20InRange(t *testing.T) {
	// v3: 范围从 ch11 扩到 ch11-20
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0.25, 20, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 900, summary.CacheTokens)
	require.Equal(t, 675, summary.Quota)
}

func TestFakeCacheLowCacheOverridden(t *testing.T) {
	// v3: 上游真实缓存 30% (<80%) → 覆盖为 90%（v2 是 !=0 即保留，v3 起低命中也替换）
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 300, 0.25, 12, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 900, summary.CacheTokens)
	// (1000-900) + 900*0.25 = 325; + 350 = 675
	require.Equal(t, 675, summary.Quota)
}

func TestFakeCacheNotInjectedWhenUpstreamHasHighCache(t *testing.T) {
	// v3: 上游真实缓存 80% (=边界, 800*10 >= 1000*8) → 保留真实值
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 800, 0.25, 12, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 800, summary.CacheTokens)
	// (1000-800) + 800*0.25 = 400; + 350 = 750
	require.Equal(t, 750, summary.Quota)
}

func TestFakeCacheNotInjectedOnOtherChannel(t *testing.T) {
	// ch10/ch21 均在范围(11-20)外
	for _, ch := range []int{10, 21} {
		ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0.25, ch, relayconstant.RelayModeChatCompletions)
		summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

		require.Equal(t, 0, summary.CacheTokens, "ch%d 应被范围守卫拦截", ch)
		// 无折扣: 1000 + 350 = 1350
		require.Equal(t, 1350, summary.Quota)
	}
}

func TestFakeCacheNotInjectedOnEmbedding(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0.25, 11, relayconstant.RelayModeEmbeddings)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 0, summary.CacheTokens)
}

func TestFakeCacheNotInjectedWhenRatioIsOne(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 1.0, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 0, summary.CacheTokens)
	// ratio=1 等同无折扣: (1000-0)+0*1 = 1000; + 350 = 1350
	require.Equal(t, 1350, summary.Quota)
}

func TestFakeCacheNotInjectedWhenRatioIsZero(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 0, summary.CacheTokens)
	// CacheRatio=0 注入反而免费，guard 拦住: 1000 + 350 = 1350
	require.Equal(t, 1350, summary.Quota)
}

func TestFakeCacheZeroPrompt(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(0, 0, 0.25, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// prompt=0 → TotalTokens=100>0 但 fake 分支不打: 0 + 350 = 350
	require.Equal(t, 0, summary.CacheTokens)
	require.Equal(t, 350, summary.Quota)
}
