package openai

// isOpenAICompatValidEffort reports whether *effort* is an effort level that
// OpenAI-compatible upstreams (GLM, 魔搭, 商汤, vLLM, SGLang …) accept on the
// top-level “reasoning_effort“ field. Those upstreams validate the value
// against {low, high, max} and 400 on anything else ("none", "medium",
// "minimal", "default", "ultra", garbage). Lower-cased here so a client
// sending "MAX" or "High" is not punished for casing.
//
// [patch lou #13] See adaptor.go ConvertOpenAIRequest: non-o/gpt-5 models run
// this check so a relay-fronted reasoning model never 400s on an effort the
// caller's own default filled in (Hermes custom profile → "medium"; title
// generation → "none").
func isOpenAICompatValidEffort(effort string) bool {
	switch effort {
	case "low", "high", "max", "Low", "High", "Max", "LOW", "HIGH", "MAX":
		return true
	}
	return false
}
