package controller

// [patch lou #10] setting 合并保留单测——钉死三个不变式：
// 1. Web UI 默认模板（不含后端自定义字段如 hedging_*）不得覆盖丢失已有自定义字段
// 2. 明示更新照常生效（API 传完整 setting / 显式传某字段值覆盖旧值）
// 3. JSON 非法时返回错误（调用方降级为官方原语义，不阻断更新）

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 场景1：默认模板不丢后端字段——楼先生 20261007 实况：新增 key 后 hedging_* 全被覆盖丢失
func TestMergeChannelSetting_PreservesBackendFields(t *testing.T) {
	origin := `{"force_format":false,"thinking_to_content":false,"proxy":"","pass_through_body_enabled":false,"system_prompt":"","system_prompt_override":false,"hedging_enabled":true,"hedging_delays_ms":[8000],"hedging_ratio":90}`
	// Web UI 前端表单默认序列化：只有 6 个基础字段，不含 hedging_*
	incoming := `{"force_format":false,"thinking_to_content":false,"proxy":"","pass_through_body_enabled":false,"system_prompt":"","system_prompt_override":false}`

	merged, err := mergeChannelSetting(origin, incoming)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(merged), &m))

	// 后端自定义字段全部保留
	assert.JSONEq(t, `true`, string(m["hedging_enabled"]))
	assert.JSONEq(t, `[8000]`, string(m["hedging_delays_ms"]))
	assert.JSONEq(t, `90`, string(m["hedging_ratio"]))
	// 前端管理的字段照常存在
	assert.Contains(t, m, "force_format")
}

// 场景2：明示覆盖生效——想关 hedging 就显式传 false（合并不能只进不出）
func TestMergeChannelSetting_ExplicitOverrideWins(t *testing.T) {
	origin := `{"force_format":true,"hedging_enabled":true,"hedging_delays_ms":[8000]}`
	incoming := `{"force_format":false,"hedging_enabled":false}`

	merged, err := mergeChannelSetting(origin, incoming)
	require.NoError(t, err)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(merged), &m))
	assert.JSONEq(t, `false`, string(m["force_format"]), "前端字段更新生效")
	assert.JSONEq(t, `false`, string(m["hedging_enabled"]), "显式关闭生效")
	assert.JSONEq(t, `[8000]`, string(m["hedging_delays_ms"]), "未传字段保留")
}

// 场景3：空值短路
func TestMergeChannelSetting_EmptyShortCircuit(t *testing.T) {
	incoming := `{"force_format":true}`
	merged, err := mergeChannelSetting("", incoming)
	require.NoError(t, err)
	assert.JSONEq(t, incoming, merged, "origin 空时 incoming 原样返回")

	origin := `{"hedging_enabled":true}`
	merged, err = mergeChannelSetting(origin, "")
	require.NoError(t, err)
	assert.JSONEq(t, origin, merged, "incoming 空时 origin 原样返回")
}

// 场景4：非法 JSON 报错（调用方降级官方原语义）
func TestMergeChannelSetting_InvalidJSON(t *testing.T) {
	_, err := mergeChannelSetting(`{"a":`, `{"b":1}`)
	assert.Error(t, err, "origin 非法应报错")

	_, err = mergeChannelSetting(`{"a":1}`, `not-json`)
	assert.Error(t, err, "incoming 非法应报错")
}
