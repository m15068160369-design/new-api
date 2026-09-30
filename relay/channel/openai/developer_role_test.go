package openai

// [patch lou #7] developer→system role 降级的测试。
// 覆盖：非 OpenAI 推理模型（glm/deepseek 等）developer→system 全消息扫描、
// 多条 developer 全部转换、非 developer role 不动、content 保持不变、
// OpenAI 推理模型（o3/gpt-5）不进此分支（system→developer 原逻辑保留）、
// 空消息列表安全。

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func make7Info(upstreamModel string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: upstreamModel},
		RelayMode:   relayconstant.RelayModeChatCompletions,
	}
}

func TestDeveloperRoleDowngradedToSystem(t *testing.T) {
	// glm 模型：developer 应降级为 system
	info := make7Info("glm-5.3")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "glm-5.3",
		Messages: []dto.Message{
			{Role: "developer", Content: "你是助手"},
			{Role: "user", Content: "hi"},
		},
	}
	c, _ := gin.CreateTestContext(nil)
	result, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	req, ok := result.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "system", req.Messages[0].Role)
	require.Equal(t, "你是助手", req.Messages[0].Content)
	require.Equal(t, "user", req.Messages[1].Role)
}

func TestDeveloperRoleMultipleAllConverted(t *testing.T) {
	// 多条 developer（首条+中段）全部转换
	info := make7Info("zai-org/GLM-5.2")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "glm",
		Messages: []dto.Message{
			{Role: "developer", Content: "A"},
			{Role: "user", Content: "q"},
			{Role: "assistant", Content: "a"},
			{Role: "developer", Content: "B"},
			{Role: "user", Content: "q2"},
		},
	}
	c, _ := gin.CreateTestContext(nil)
	result, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	req := result.(*dto.GeneralOpenAIRequest)
	require.Equal(t, "system", req.Messages[0].Role)
	require.Equal(t, "system", req.Messages[3].Role)
	require.Equal(t, "user", req.Messages[1].Role)
	require.Equal(t, "assistant", req.Messages[2].Role)
}

func TestOtherRolesUntouched(t *testing.T) {
	// system/assistant/user/tool 原样透传
	info := make7Info("deepseek-v4-pro-0813")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "deepseek",
		Messages: []dto.Message{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "u"},
			{Role: "assistant", Content: "a"},
			{Role: "user", Content: "u2"},
		},
	}
	c, _ := gin.CreateTestContext(nil)
	result, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	req := result.(*dto.GeneralOpenAIRequest)
	require.Equal(t, "system", req.Messages[0].Role)
	require.Equal(t, "user", req.Messages[1].Role)
	require.Equal(t, "assistant", req.Messages[2].Role)
	require.Equal(t, "user", req.Messages[3].Role)
}

func TestOModelStillSystemToDeveloper(t *testing.T) {
	// o3 模型：原 system→developer 逻辑保留，不进降级分支
	info := make7Info("o3-mini")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "o3-mini",
		Messages: []dto.Message{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "u"},
		},
	}
	c, _ := gin.CreateTestContext(nil)
	result, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	req := result.(*dto.GeneralOpenAIRequest)
	require.Equal(t, "developer", req.Messages[0].Role)
}

func TestOModelClientDeveloperPreserved(t *testing.T) {
	// o3 模型：客户端自己发的 developer 不动（上游本来就认）
	info := make7Info("o3")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{
		Model: "o3",
		Messages: []dto.Message{
			{Role: "developer", Content: "s"},
			{Role: "user", Content: "u"},
		},
	}
	c, _ := gin.CreateTestContext(nil)
	result, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	req := result.(*dto.GeneralOpenAIRequest)
	require.Equal(t, "developer", req.Messages[0].Role)
}

func TestEmptyMessagesSafe(t *testing.T) {
	// 空消息列表不 panic
	info := make7Info("glm-5.3")
	a := &Adaptor{}
	request := &dto.GeneralOpenAIRequest{Model: "glm-5.3", Messages: []dto.Message{}}
	c, _ := gin.CreateTestContext(nil)
	_, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
}