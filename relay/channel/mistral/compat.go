package mistral

import (
	"github.com/QuantumNous/new-api/dto"
)

// [patch lou #19] Mistral 渠道请求全面兼容层。
//
// 目标：客户端无论发什么 OpenAI 风格请求，到 Mistral 上游都合法——
// 能映射的映射（seed→random_seed），能转发的转发（stop/response_format/
// logprobs/parallel_tool_calls），越界的 clamp 到 Mistral 合法域。
// 依据 docs.mistral.ai/openapi.yaml（2026-11-16 拉取）：
//
//	temperature        number [0, 1.5]
//	top_p              number (0, 1]  exclusiveMinimum
//	frequency_penalty  number [-2, 2]
//	presence_penalty   number [-2, 2]
//	n                  integer ≥1
//	random_seed        integer ≥0（OpenAI 的 seed 字段名不同，必须改名）
//	response_format    {type: text|json_object|json_schema, json_schema?}
//
// 原则：只放宽客户端限制，不砍功能；clamp 只在越界时发生，界内值零改动。
// tools[].function 是强类型 FunctionRequest，序列化只会出 name/description/
// parameters，无额外键泄漏，无需重建。

// clampFloat 把 v 夹到 [lo, hi]。nil 原样返回（不发字段走上游默认）。
func clampFloat(v *float64, lo, hi float64) *float64 {
	if v == nil {
		return nil
	}
	if *v < lo {
		n := lo
		return &n
	}
	if *v > hi {
		n := hi
		return &n
	}
	return v
}

// sanitizeContentChunks [patch lou #19c] 消息层 chunk 白名单：Mistral
// ContentChunk oneOf 只认 text/image_url/document_url/reference/file/thinking。
// 客户端可能发 input_audio（OpenAI 语音）/ video_url（阿里系）等 chunk，
// 原样序列化会 422 extra_forbidden/oneOf 失配。策略：
//   - text chunk：保留，text 为空时补空串（TextChunk required:[text]，
//     dto omitempty 会把空串吞掉造成 422）
//   - image_url：保留（上游已转 url 字符串形态，ImageURLChunk 接受 string）
//   - 其余类型（input_audio/video_url/file 等）：丢弃，不拒请求
//   - 全部被丢弃/空列表：退化为单个空 text chunk（所有 role 接受 string 形态）
func sanitizeContentChunks(chunks []dto.MediaContent) []dto.MediaContent {
	out := make([]dto.MediaContent, 0, len(chunks))
	for _, c := range chunks {
		switch c.Type {
		case dto.ContentTypeText:
			out = append(out, dto.MediaContent{Type: dto.ContentTypeText, Text: c.Text})
		case dto.ContentTypeImageURL:
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return []dto.MediaContent{{Type: dto.ContentTypeText, Text: ""}}
	}
	return out
}

// sanitizeResponseFormat 枚举合法化：Mistral 只认 text/json_object/json_schema，
// 客户端发非常规值（如 grammar 等）会 422。非法值归 text（不施加格式约束，
// 最保守），与 reasoning_effort 非法值归 max 同一兜底精神：不打回、就近合法。
func sanitizeResponseFormat(rf *dto.ResponseFormat) *dto.ResponseFormat {
	if rf == nil {
		return nil
	}
	switch rf.Type {
	case "text", "json_object", "json_schema":
		return rf
	}
	return &dto.ResponseFormat{Type: "text"}
}

// sanitizeSamplingParams 值域合法化：temperature∈[0,1.5]、top_p∈(0,1]、
// penalties∈[-2,2]、n≥1。top_p=0 是 exclusiveMinimum 违例，用极小值保语义。
func sanitizeSamplingParams(req *dto.GeneralOpenAIRequest) {
	req.Temperature = clampFloat(req.Temperature, 0, 1.5)
	if req.TopP != nil {
		if *req.TopP <= 0 {
			eps := 1e-8
			req.TopP = &eps
		} else if *req.TopP > 1 {
			one := 1.0
			req.TopP = &one
		}
	}
	req.FrequencyPenalty = clampFloat(req.FrequencyPenalty, -2, 2)
	req.PresencePenalty = clampFloat(req.PresencePenalty, -2, 2)
	if req.N != nil && *req.N < 1 {
		one := 1
		req.N = &one
	}
}

// sanitizeToolChoice 对象形态只留 function.name：Mistral FunctionName 是
// additionalProperties:false，客户端若塞 description 等会 422。
// （已并入 convertMistralToolChoice，见 text.go）
