package mistral

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

// [patch lou #16] gin context key：ConvertOpenAIRequest 存缓存亲和 key，
// SetupRequestHeader 取出镜像进 x-affinity header。前缀带包名避免冲突。
const mistralAffinityCtxKey = "mistral_affinity_key"

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	//TODO implement me
	panic("implement me")
	return nil, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, info.RequestURLPath, info.ChannelType), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	mirrorAffinityHeader(c, req)
	return nil
}

// [patch lou #16] 镜像缓存亲和 key 进 x-affinity header（vibe 端点语义；
// kaecho/mistral 实践：prompt_cache_key 参数 + x-affinity header 双通道，
// Mistral 负载均衡据此把同会话请求钉在持有前缀缓存的节点）。
// 独立小函数便于单测（不依赖 gin.Context 的请求级初始化）。
func mirrorAffinityHeader(c *gin.Context, req *http.Header) {
	if key := c.GetString(mistralAffinityCtxKey); key != "" {
		req.Set("x-affinity", key)
	}
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	mistralReq, affinityKey := requestOpenAI2Mistral(request)
	// [patch lou #16] 缓存亲和 key 存 gin context，SetupRequestHeader 里镜像进 x-affinity
	if affinityKey != "" {
		c.Set(mistralAffinityCtxKey, affinityKey)
	}
	return mistralReq, nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// [patch lou #15a] Mistral 思考模型返回 chunklet 数组 content，此处归一为标准
	// OpenAI 形态（thinking→reasoning_content、text→content）后再走通用 handler，
	// 严格校验的客户端与 processTokenData 计费路径从此看到标准帧。
	if info.IsStream {
		resp.Body = NewNormalizeStream(resp.Body)
		usage, err = openai.OaiStreamHandler(c, info, resp)
	} else {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			normalized := NormalizeChatCompletionBody(body)
			resp.Body = io.NopCloser(bytes.NewReader(normalized))
			resp.ContentLength = int64(len(normalized))
			resp.Header.Set("Content-Length", strconv.Itoa(len(normalized)))
		} else {
			// 读失败保守处理：把已读部分原样回灌
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
		usage, err = openai.OpenaiHandler(c, info, resp)
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
