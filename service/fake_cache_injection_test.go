package service

// [patch lou #6 v3-v6] 假缓存注入的测试矩阵。
// 覆盖 calculateTextQuotaSummary 中 ch11-15 假缓存注入的每个 guard 分支：
// 渠道范围（ch10/ch16 边界外、ch15 边界内）、RelayMode、上游缓存命中率
// <80% 覆盖为 v6 双参 a·p−D 值、≥80% 保留真实值、CacheRatio 边界（1/0）、零 prompt。
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
	// v6 双参曲线: p=2000 → 注入 994（v3 时代期望 900/675 是固定 90% 旧值）
	ctx, relayInfo, usage := makeFakeCacheTestSetup(2000, 0, 0.25, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// fake cache = 994; (2000-994) + 994*0.25 = 1254.5; + 100*3.5 = 1604.5 → Round → 1605
	require.Equal(t, 994, summary.CacheTokens)
	require.Equal(t, 1605, summary.Quota)
}

func TestFakeCacheInjectionCh15InRange(t *testing.T) {
	// v3: 范围从 ch11 扩到 ch11-20；20261115 楼令收窄到 ch11-15（ch15 = 新上界）
	ctx, relayInfo, usage := makeFakeCacheTestSetup(2000, 0, 0.25, 15, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// v6 双参曲线: p=2000 → 994; (2000-994) + 994*0.25 = 1254.5; + 350 = 1604.5 → Round → 1605
	require.Equal(t, 994, summary.CacheTokens)
	require.Equal(t, 1605, summary.Quota)
}

func TestFakeCacheLowCacheOverridden(t *testing.T) {
	// 上游真实缓存 30% (600/2000, <80%) → 覆盖为 v6 双参值（v2 是 !=0 即保留，v3 起低命中也替换）
	ctx, relayInfo, usage := makeFakeCacheTestSetup(2000, 600, 0.25, 12, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 994, summary.CacheTokens)
	// (2000-994) + 994*0.25 = 1254.5; + 350 = 1604.5 → Round → 1605
	require.Equal(t, 1605, summary.Quota)
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
	// ch10/ch16 均在范围(11-15)外
	for _, ch := range []int{10, 16} {
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
