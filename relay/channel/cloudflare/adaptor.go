package cloudflare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	//TODO implement me
	panic("implement me")
	return nil, nil
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

// [patch lou #5] splitCfKey: per-key account id for Cloudflare multi-key channels.
// Key line format "ACCOUNT_ID----API_TOKEN" (4-dash, as shipped by key sellers) or
// "ACCOUNT_ID---API_TOKEN" (3-dash). Returns empty accountID for plain tokens so
// the channel-level Account ID field (legacy path) still applies.
// Package-private on purpose: this format is a Cloudflare-only convention and must
// never leak into other channel types (their keys may legitimately contain dashes).
func splitCfKey(key string) (accountID, token string) {
	key = strings.TrimSpace(key)
	for _, sep := range []string{"----", "---"} {
		if i := strings.Index(key, sep); i > 0 && i+len(sep) < len(key) {
			acc := strings.TrimSpace(key[:i])
			tok := strings.TrimSpace(key[i+len(sep):])
			if acc != "" && tok != "" {
				return acc, tok
			}
		}
	}
	return "", key
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// [patch lou #5] account id: per-key "ACCOUNT----TOKEN" line wins, then
	// channel-level Account ID (stored in Other -> ApiVersion), unchanged legacy path.
	account := info.ApiVersion
	if acc, _ := splitCfKey(info.ApiKey); acc != "" {
		account = acc
	}
	switch info.RelayMode {
	case constant.RelayModeChatCompletions:
		return fmt.Sprintf("%s/client/v4/accounts/%s/ai/v1/chat/completions", info.ChannelBaseUrl, account), nil
	case constant.RelayModeEmbeddings:
		return fmt.Sprintf("%s/client/v4/accounts/%s/ai/v1/embeddings", info.ChannelBaseUrl, account), nil
	case constant.RelayModeResponses:
		return fmt.Sprintf("%s/client/v4/accounts/%s/ai/v1/responses", info.ChannelBaseUrl, account), nil
	default:
		return fmt.Sprintf("%s/client/v4/accounts/%s/ai/run/%s", info.ChannelBaseUrl, account, info.UpstreamModelName), nil
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	// [patch lou #5] strip ACCOUNT_ID---- prefix if present; plain keys pass through unchanged.
	_, token := splitCfKey(info.ApiKey)
	req.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	case constant.RelayModeCompletions:
		return convertCf2CompletionsRequest(*request), nil
	default:
		return request, nil
	}
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	// 添加文件字段
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		return nil, errors.New("file is required")
	}
	defer file.Close()
	// 打开临时文件用于保存上传的文件内容
	requestBody := &bytes.Buffer{}

	// 将上传的文件内容复制到临时文件
	if _, err := io.Copy(requestBody, file); err != nil {
		return nil, err
	}
	return requestBody, nil
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case constant.RelayModeEmbeddings:
		fallthrough
	case constant.RelayModeChatCompletions:
		if info.IsStream {
			err, usage = cfStreamHandler(c, info, resp)
		} else {
			err, usage = cfHandler(c, info, resp)
		}
	case constant.RelayModeResponses:
		if info.IsStream {
			usage, err = openai.OaiResponsesStreamHandler(c, info, resp)
		} else {
			usage, err = openai.OaiResponsesHandler(c, info, resp)
		}
	case constant.RelayModeAudioTranslation:
		fallthrough
	case constant.RelayModeAudioTranscription:
		err, usage = cfSTTHandler(c, info, resp)
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
