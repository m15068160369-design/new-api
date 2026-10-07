package channel

// [patch lou #9] 流式请求对冲（Hedged Request）——双路竞速，首字最快者胜。
//
// 动机：NIM 免费池首字排队随机性大（实测 p50=15.6s、p95=71.6s），单路请求赌一把，
// 慢就慢到底。对冲用概率碾压排队抖动：第一路发出后，若 HedgingDelaysMs 档位到点
// （或该路秒回快失败）仍无有效首字，则换 key 补发下一路；任一路先产出首个非空
// delta.content / reasoning_content / tool_calls 即胜出，其余路关闭连接止损
// （流式断 TCP，上游停止生成，只计已收 token）。
//
// 设计约束（楼先生拍板）：
//   - 只做流式；非流式走原逻辑
//   - 第二路重新随机选 key（multi-key 池 110~400 个，撞同 key 概率 ~1%，不强制排除）
//   - HedgingRatio% 走对冲、其余走原路径——被对冲掩盖的坏 key 仍会经原路径暴露进
//     渠道错误统计（90/10 采样监控，解决统计盲区）
//   - 补发计划 HedgingDelaysMs 为数组：[8000]=双路；[8000,18000]=三路（8s 补路2、
//     18s 补路3）。扩路数只改渠道配置，不改代码。
//
// 爆炸半径：shouldHedge 默认 false——未配置 hedging 的渠道、非流式请求、10% 采样
// 全部走原路径，行为零改动。仅「渠道显式配置 + 流式 + 概率命中」进入本文件逻辑。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	common2 "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

const (
	hedgeDefaultRatio    = 90
	hedgeDefaultDelayMs  = 8000
	hedgeFirstContentBuf = 64 << 10 // peek 逐行读取上限，超出按无首字处理
)

// shouldHedge 判定本次请求是否进入对冲路径。
// 全部满足才走：流式 + 渠道开启 + delays 非空 + 概率命中。
func shouldHedge(info *common.RelayInfo) bool {
	if info == nil || info.ChannelMeta == nil {
		return false
	}
	if !info.IsStream {
		return false
	}
	s := info.ChannelSetting
	if !s.HedgingEnabled || len(s.HedgingDelaysMs) == 0 {
		return false
	}
	ratio := s.HedgingRatio
	if ratio <= 0 {
		ratio = hedgeDefaultRatio
	}
	if ratio > 100 {
		ratio = 100
	}
	return rand.Intn(100) < ratio
}

// hedgeDelays 解析渠道配置的补发计划，返回合法的绝对延迟序列。
func hedgeDelays(info *common.RelayInfo) []time.Duration {
	raw := info.ChannelSetting.HedgingDelaysMs
	delays := make([]time.Duration, 0, len(raw))
	for _, ms := range raw {
		if ms > 0 {
			delays = append(delays, time.Duration(ms)*time.Millisecond)
		}
	}
	if len(delays) == 0 {
		delays = append(delays, time.Duration(hedgeDefaultDelayMs)*time.Millisecond)
	}
	return delays
}

type hedgePeekResult struct {
	buffered []byte
	found    bool
	err      error
}

type hedgeEvent struct {
	routeIdx int
	result   hedgePeekResult
}

// hedgeRoute 单路状态。reader 仅在 200 流式路上有值，供胜出后接续下游读取。
type hedgeRoute struct {
	resp   *http.Response
	reader *bufio.Reader
	dead   bool
	index  int
}

// hedgeBody 把「已缓冲首字字节 + bufio 剩余流」拼成下游可读的 ReadCloser，
// Close 转发给原始响应体（断 TCP 止损）。
type hedgeBody struct {
	io.Reader
	closer io.Closer
}

func (b *hedgeBody) Close() error { return b.closer.Close() }

// hedgeWrapResp 用探测期间读出的 buffered 前缀 + bufio 剩余流重建响应体。
// bufio 内部 buffer 含预读字节，MultiReader 无缝衔接，下游流内容与原流逐字节一致。
func hedgeWrapResp(resp *http.Response, buffered []byte, reader io.Reader) *http.Response {
	orig := resp.Body
	resp.Body = &hedgeBody{
		Reader: io.MultiReader(bytes.NewReader(buffered), reader),
		closer: orig,
	}
	return resp
}

// hedgeHasContent 判断一条 SSE data 载荷是否含有效首字：
// 首个非空 delta.content / reasoning_content / tool_calls。
// role chunk、SSE ping、错误信封、非 JSON 行都不算。
func hedgeHasContent(data string) bool {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          *string           `json:"content"`
				ReasoningContent *string           `json:"reasoning_content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := common2.Unmarshal([]byte(data), &chunk); err != nil {
		return false
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			return true
		}
		if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
			return true
		}
		if len(ch.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// hedgePeek 逐行读取 SSE 流直到：读到有效首字 / 流结束 / 读错误 / 超过上限。
// 所有读出的字节缓存在返回值 buffered，供胜出后重建流。
func hedgePeek(reader *bufio.Reader, out chan<- hedgePeekResult) {
	defer func() {
		if r := recover(); r != nil {
			out <- hedgePeekResult{err: fmt.Errorf("hedge peek panic: %v", r)}
		}
	}()
	var buf bytes.Buffer
	total := 0
	for {
		line, err := reader.ReadString('\n')
		buf.WriteString(line)
		total += len(line)
		if line != "" {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload != "" && payload != "[DONE]" && hedgeHasContent(payload) {
					out <- hedgePeekResult{buffered: buf.Bytes(), found: true}
					return
				}
			}
		}
		if err != nil {
			out <- hedgePeekResult{buffered: buf.Bytes(), err: err}
			return
		}
		if total > hedgeFirstContentBuf {
			out <- hedgePeekResult{buffered: buf.Bytes(), err: fmt.Errorf("hedge peek exceeded %d bytes without first content", hedgeFirstContentBuf)}
			return
		}
	}
}

// hedgeNextKey 为补发路重新选 key。仅 multi-key 渠道；失败返回空串（沿用原 key）。
func hedgeNextKey(info *common.RelayInfo) string {
	if !info.ChannelIsMultiKey {
		return ""
	}
	ch, err := model.CacheGetChannel(info.ChannelId)
	if err != nil || ch == nil {
		return ""
	}
	key, _, keyErr := ch.GetNextEnabledKey()
	if keyErr != nil || key == "" {
		return ""
	}
	return key
}

// hedgeSendOnce 构造并发送单路请求（复用 DoApiRequest 原构造链）。
// freshKey 非空时临时替换 info.ApiKey 走一遍 SetupRequestHeader，构造完立即恢复。
func hedgeSendOnce(a Adaptor, c *gin.Context, info *common.RelayInfo, body []byte, freshKey string) (*http.Response, error) {
	fullRequestURL, err := a.GetRequestURL(info)
	if err != nil {
		return nil, fmt.Errorf("get request url failed: %w", err)
	}
	req, err := http.NewRequest(c.Request.Method, fullRequestURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}
	req.ContentLength = int64(len(body))
	headers := req.Header
	origKey := info.ApiKey
	if freshKey != "" {
		info.ApiKey = freshKey
	}
	err = a.SetupRequestHeader(c, &headers, info)
	if freshKey != "" {
		info.ApiKey = origKey
	}
	if err != nil {
		return nil, fmt.Errorf("setup request header failed: %w", err)
	}
	headerOverride, err := processHeaderOverride(info, c)
	if err != nil {
		return nil, err
	}
	applyHeaderOverrideToRequest(req, headerOverride)
	return doRequest(c, req, info)
}

// hedgedDoApiRequest 对冲主循环。delays 为绝对时刻（自请求发起）。
// 触发下一路条件（任一）：delays[di] 到点仍无胜者 / 任一路已废（快失败立即补）。
// 任一路 found 即胜出；全部发完且全废则返回最有信息量的失败
// （优先带 body 的非 200 响应，其次最后一个错误）。
func hedgedDoApiRequest(a Adaptor, c *gin.Context, info *common.RelayInfo, body []byte) (*http.Response, error) {
	delays := hedgeDelays(info)
	t0 := time.Now()
	eventCh := make(chan hedgeEvent, len(delays)+1)
	routes := make([]*hedgeRoute, 0, len(delays)+1)

	failedResp := (*http.Response)(nil) // 全败时优先返回的非 200 响应（已拼回流）
	lastErr := error(nil)

	startRoute := func() {
		idx := len(routes)
		r := &hedgeRoute{index: idx}
		routes = append(routes, r)
		freshKey := ""
		if idx > 0 {
			freshKey = hedgeNextKey(info)
		}
		resp, err := hedgeSendOnce(a, c, info, body, freshKey)
		if err != nil {
			logger.LogDebug(c, "[hedging] route %d send failed: %s", idx+1, err.Error())
			r.dead = true
			lastErr = err
			eventCh <- hedgeEvent{routeIdx: idx, result: hedgePeekResult{}}
			return
		}
		r.resp = resp
		if resp.StatusCode != http.StatusOK {
			logger.LogDebug(c, "[hedging] route %d fast-fail status=%d", idx+1, resp.StatusCode)
			r.dead = true
			if failedResp == nil {
				// 非 200：body 未被 peek，完整保留供全败时返回
				failedResp = hedgeWrapResp(resp, nil, bufio.NewReader(resp.Body))
			} else {
				_ = resp.Body.Close()
			}
			eventCh <- hedgeEvent{routeIdx: idx, result: hedgePeekResult{}}
			return
		}
		r.reader = bufio.NewReader(resp.Body)
		go func() {
			ch := make(chan hedgePeekResult, 1)
			hedgePeek(r.reader, ch)
			res := <-ch
			eventCh <- hedgeEvent{routeIdx: idx, result: res}
		}()
	}

	var winner *hedgeRoute
	var winnerBuf []byte

	startRoute() // 路1

	di := 0
	for {
		if winner != nil {
			break
		}
		// 补路判定：到点 或 存在废路（快失败立即补）
		if di < len(delays) {
			hasDead := false
			for _, r := range routes {
				if r.dead {
					hasDead = true
					break
				}
			}
			if hasDead || time.Since(t0) >= delays[di] {
				startRoute()
				di++
				continue
			}
		}
		// 全部发完全部废 → 退出
		if di >= len(delays) {
			allDead := true
			for _, r := range routes {
				if !r.dead && r.resp != nil {
					allDead = false
					break
				}
			}
			if allDead {
				break
			}
		}
		// 等待：下一档 timer 或 路 event
		var timerC <-chan time.Time
		var timer *time.Timer
		if di < len(delays) {
			wait := delays[di] - time.Since(t0)
			if wait < 0 {
				wait = 0
			}
			timer = time.NewTimer(wait)
			timerC = timer.C
		}
		select {
		case ev := <-eventCh:
			if timer != nil {
				timer.Stop()
			}
			if ev.routeIdx < 0 || ev.routeIdx >= len(routes) {
				continue
			}
			r := routes[ev.routeIdx]
			if ev.result.found {
				winner = r
				winnerBuf = ev.result.buffered
				continue
			}
			// 该路废（EOF/读错误；发送失败与非200已在 startRoute 标记 dead）
			if !r.dead {
				r.dead = true
				if ev.result.err != nil {
					lastErr = ev.result.err
				}
			}
		case <-timerC:
			// 循环头补路
		}
	}

	// 收尾：关闭所有非胜出路、非 failedResp 路的连接（断 TCP，上游停止生成）
	for _, r := range routes {
		if r == winner || r.resp == nil {
			continue
		}
		if failedResp != nil && r.resp == failedResp {
			continue
		}
		_ = r.resp.Body.Close()
	}
	if winner != nil {
		logger.LogInfo(c, fmt.Sprintf("[hedging] winner=route%d ttft=%.2fs routes=%d", winner.index+1, time.Since(t0).Seconds(), len(routes)))
		// winner.reader 含 peek 预读的剩余字节；buffered 是首字行前缀；MultiReader 无缝
		return hedgeWrapResp(winner.resp, winnerBuf, winner.reader), nil
	}
	if failedResp != nil {
		return failedResp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("hedged request: all %d routes failed without response", len(routes))
	}
	return nil, lastErr
}
