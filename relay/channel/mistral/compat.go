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
