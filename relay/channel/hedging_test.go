package channel

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [patch lou #9] hedging 单测——保护首字判定、配置解析、流重建（peek+wrap 无缝拼接）三处核心不变式。

func TestHedgeHasContent(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"content 非空", `{"choices":[{"delta":{"content":"hi"}}]}`, true},
		{"reasoning_content 非空", `{"choices":[{"delta":{"reasoning_content":"思考"}}]}`, true},
		{"tool_calls 非空", `{"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}`, true},
		{"role chunk(无内容)", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"content 空串", `{"choices":[{"delta":{"content":""}}]}`, false},
		{"tool_calls 空数组", `{"choices":[{"delta":{"tool_calls":[]}}]}`, false},
		{"无 choices(error信封)", `{"error":{"message":"rate limited"}}`, false},
		{"非 JSON", `: keepalive`, false},
		{"空串", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hedgeHasContent(c.data))
		})
	}
}

func TestShouldHedge(t *testing.T) {
	makeInfo := func(stream bool, setting dto.ChannelSettings) *common.RelayInfo {
		return &common.RelayInfo{
			ChannelMeta: &common.ChannelMeta{ChannelSetting: setting},
			IsStream:    stream,
		}
	}
	t.Run("nil info 安全", func(t *testing.T) {
		assert.False(t, shouldHedge(nil))
	})
	t.Run("ChannelMeta nil 安全", func(t *testing.T) {
		assert.False(t, shouldHedge(&common.RelayInfo{IsStream: true}))
	})
	t.Run("非流式不进", func(t *testing.T) {
		info := makeInfo(false, dto.ChannelSettings{HedgingEnabled: true, HedgingDelaysMs: []int{8000}, HedgingRatio: 100})
		assert.False(t, shouldHedge(info))
	})
	t.Run("未配置不进", func(t *testing.T) {
		info := makeInfo(true, dto.ChannelSettings{})
		assert.False(t, shouldHedge(info))
	})
	t.Run("delays 空不进", func(t *testing.T) {
		info := makeInfo(true, dto.ChannelSettings{HedgingEnabled: true, HedgingRatio: 100})
		assert.False(t, shouldHedge(info))
	})
	t.Run("开启+流式+ratio100 必走", func(t *testing.T) {
		info := makeInfo(true, dto.ChannelSettings{HedgingEnabled: true, HedgingDelaysMs: []int{8000}, HedgingRatio: 100})
		assert.True(t, shouldHedge(info))
	})
}

func TestHedgeDelays(t *testing.T) {
	t.Run("配置双档", func(t *testing.T) {
		info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{HedgingDelaysMs: []int{8000, 18000}},
		}}
		d := hedgeDelays(info)
		require.Len(t, d, 2)
		assert.Equal(t, 8*time.Second, d[0])
		assert.Equal(t, 18*time.Second, d[1])
	})
	t.Run("过滤非法后空→默认", func(t *testing.T) {
		info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{
			ChannelSetting: dto.ChannelSettings{HedgingDelaysMs: []int{-1, 0}},
		}}
		d := hedgeDelays(info)
		require.Len(t, d, 1)
		assert.Equal(t, 8*time.Second, d[0])
	})
	t.Run("空配置→默认", func(t *testing.T) {
		info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{}}
		d := hedgeDelays(info)
		require.Len(t, d, 1)
		assert.Equal(t, 8*time.Second, d[0])
	})
}

// TestHedgePeekAndWrap 核心不变式：peek 读到首字后，wrapResp 重建的流
// 与原始流逐字节一致（含 peek 跳过的 role/ping 行 + bufio 预读的后续行）。
// 这是下游 OaiStreamHandler 拿到的流不能损坏的硬保证。
func TestHedgePeekAndWrap(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"世界\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	reader := bufio.NewReader(resp.Body)

	ch := make(chan hedgePeekResult, 1)
	hedgePeek(reader, ch)
	res := <-ch
	require.True(t, res.found, "应识别 content 首字")
	// buffered 含 role 行 + 首字行（到 content 行为止）
	assert.Contains(t, string(res.buffered), "assistant")
	assert.Contains(t, string(res.buffered), "你好")

	wrapped := hedgeWrapResp(resp, res.buffered, reader)
	out, err := io.ReadAll(wrapped.Body)
	require.NoError(t, err)
	// 重建流必须与原流逐字节一致——验证 MultiReader+bufio 无缝拼接
	assert.Equal(t, body, string(out), "wrapResp 重建流必须与原流完全一致")
	// 关闭不 panic
	assert.NotPanics(t, func() { _ = wrapped.Body.Close() })
}

// TestHedgePeekNoContent 首字判定边界：只有 role/ping 行后 EOF，应 found=false。
func TestHedgePeekNoContent(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		": keepalive\n\n"
	reader := bufio.NewReader(strings.NewReader(body))
	ch := make(chan hedgePeekResult, 1)
	hedgePeek(reader, ch)
	res := <-ch
	assert.False(t, res.found)
	assert.NotNil(t, res.err) // EOF
}
