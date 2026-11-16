package mistral

import (
	"bytes"
	"encoding/base64"
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

// [patch lou #16d] 镜像缓存亲和 key 进 x-affinity header（官方 mistral-vibe
// 唯一缓存通道）。#16d 起值已是 UUID（hex 纯 ASCII）直接透传；防御性保留
// base64url 兜底：万一 ctx 里出现非 ASCII 值（老路径残留），编码后再发——
// HTTP header 只允许 ASCII，中文明文进 header 会让上游 HTTP 层 500
// （2026-11-13 #16c 生产实测）。
// 独立小函数便于单测（不依赖 gin.Context 的请求级初始化）。
func mirrorAffinityHeader(c *gin.Context, req *http.Header) {
	key := c.GetString(mistralAffinityCtxKey)
	if key == "" {
		return
	}
	if !isASCII(key) {
		key = base64.RawURLEncoding.EncodeToString([]byte(key))
	}
	req.Set("x-affinity", key)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
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
