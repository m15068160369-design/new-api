package channel

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hedgeMockAdaptor 嵌入 Adaptor 接口（未实现方法为 nil），仅实现 hedging 用到的两个。
type hedgeMockAdaptor struct {
	Adaptor
	urlFn func() string
}

func (m *hedgeMockAdaptor) GetRequestURL(info *common.RelayInfo) (string, error) {
	return m.urlFn(), nil
}

func (m *hedgeMockAdaptor) SetupRequestHeader(c *gin.Context, headers *http.Header, info *common.RelayInfo) error {
	headers.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

// TestHedgedAsyncParallel 钉死异步并发行为（lou patch #9 v3 修复的 bug 场景）：
// 路1 命中 hold header 的慢上游（3s 不回响应头），delays[0]=800ms 到点必须立即补发
// 路2（快上游秒回首字）——两路并行竞速，总耗时 ~1s 而非串行等待 3s+。
// 修复前的同步版：startRoute 阻塞在等响应头，timer 永不触发，串行死等。
func TestHedgedAsyncParallel(t *testing.T) {
	service.InitHttpClient() // 测试环境初始化全局出站 client（生产由 main 初始化）
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // hold 响应头 3s（拥塞上游模拟）
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"slow-win\"}}]}\n\n"))
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fast-win\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer fast.Close()

	var calls int32
	adaptor := &hedgeMockAdaptor{urlFn: func() string {
		if atomic.AddInt32(&calls, 1) == 1 {
			return slow.URL // 路1 → 慢上游
		}
		return fast.URL // 补发路 → 快上游
	}}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	info := &common.RelayInfo{
		IsStream: true,
		ChannelMeta: &common.ChannelMeta{
			ApiKey: "test-key",
			ChannelSetting: dto.ChannelSettings{
				HedgingEnabled: true, HedgingDelaysMs: []int{800}, HedgingRatio: 100,
			},
		},
	}
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	t0 := time.Now()
	resp, err := hedgedDoApiRequest(adaptor, c, info, body)
	elapsed := time.Since(t0)
	require.NoError(t, err)
	require.Less(t, elapsed.Seconds(), 2.5, "异步并发:路1 hold header 3s、delay=800ms 补快路,应在 ~1s 返回;串行死等(修复前)会 >3s")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(out), "fast-win", "快路应胜出")
	assert.NotContains(t, string(out), "slow-win", "慢路应被止损")
	assert.NotPanics(t, func() { _ = resp.Body.Close() })
}

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
