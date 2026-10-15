package helper

// [patch lou #11] 响应 model 字段重写（隐藏上游真实模型名）
//
// 背景：渠道 model_mapping 只改请求方向（发给上游的名）；上游响应体（流式帧/
// 非流式 body）的 model 字段带真实上游模型名原样透传给客户，暴露路由与采购
// 渠道。Web UI 用户日志侧已由 formatUserLogs 补丁隐藏，但客户端 F12 抓响应体
// 仍可见——本补丁堵最后一条暴露路径。
//
// 策略：仅当 UpstreamModelName != OriginModelName（渠道有模型映射/改名）时
// 启用重写，把发给客户端的 model 字段改回客户请求的模型名；无映射请求零开
// 销原样透传。重写用 map 反序列化改字段再序列化（与 #6b 假缓存注入同款手法），
// 保留上游响应的未知字段零丢失；解析失败一律原样透传（fail-open 不影响可用性）。
//
// 计费零影响：quota 计算走 UpstreamModelName，不经过这里的显示层重写。

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// ResponseModelRewriteNeeded 返回该请求是否需要把响应里的 model 字段重写为
// 客户请求的模型名（渠道有模型映射/改名时为 true）。
func ResponseModelRewriteNeeded(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	return info.UpstreamModelName != "" && info.OriginModelName != "" &&
		info.UpstreamModelName != info.OriginModelName
}

// DisplayModelName 返回发给客户端显示用的模型名：需要重写时用客户请求名，
// 否则保持上游名（无映射时上游名即请求名，行为与官方一致）。
func DisplayModelName(info *relaycommon.RelayInfo) string {
	if ResponseModelRewriteNeeded(info) {
		return info.OriginModelName
	}
	if info == nil {
		return ""
	}
	return info.UpstreamModelName
}
