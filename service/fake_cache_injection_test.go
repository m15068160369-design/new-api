package service

// [patch lou #6] 假缓存注入的测试矩阵。
// 覆盖 calculateTextQuotaSummary 中 ch11 假缓存注入的每个 guard 分支：
// 渠道、RelayMode、上游已报缓存、CacheRatio 边界（1/0）、零 prompt。
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

	// fake cache = 800; (1000-800) + 800*0.25 = 400; + 100*3.5 = 750
	require.Equal(t, 800, summary.CacheTokens)
	require.Equal(t, 750, summary.Quota)
}

func TestFakeCacheNotInjectedWhenUpstreamHasCache(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 300, 0.25, 11, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 300, summary.CacheTokens)
	// (1000-300) + 300*0.25 = 775; + 350 = 1125
	require.Equal(t, 1125, summary.Quota)
}

func TestFakeCacheNotInjectedOnOtherChannel(t *testing.T) {
	ctx, relayInfo, usage := makeFakeCacheTestSetup(1000, 0, 0.25, 21, relayconstant.RelayModeChatCompletions)
	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 0, summary.CacheTokens)
	// 无折扣: 1000 + 350 = 1350
	require.Equal(t, 1350, summary.Quota)
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