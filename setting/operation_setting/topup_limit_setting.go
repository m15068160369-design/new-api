package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// TopUpLimitSetting 充值防滥用限额配置。
//
// 语义（2026-11-15 拍板）：按账号维度，每个自然日内状态非 success 的充值订单
// （pending/failed/expired 全部计入，正在充值中的也算）达到上限后，拒绝该用户
// 继续创建充值订单，次日零点窗口重置；支付成功（webhook 回执落库）的订单不占额度，
// 回执迟到导致的保守计数在其状态转 success 后自动释放。
type TopUpLimitSetting struct {
	Enabled           bool `json:"enabled"`
	MaxAbnormalPerDay int  `json:"max_abnormal_per_day"`
}

// 默认配置：启用，每账号每日非成功订单上限 5。
var topUpLimitSetting = TopUpLimitSetting{
	Enabled:           true,
	MaxAbnormalPerDay: 5,
}

func init() {
	// 注册到全局配置管理器（与 checkin_setting 同机制，options 表热加载）
	config.GlobalConfig.Register("topup_limit_setting", &topUpLimitSetting)
}

// GetTopUpLimitSetting 获取充值限额配置
func GetTopUpLimitSetting() *TopUpLimitSetting {
	return &topUpLimitSetting
}

// EffectiveMaxAbnormalPerDay 返回生效的当日非成功订单上限；未启用时返回 0（不限）。
func (s *TopUpLimitSetting) EffectiveMaxAbnormalPerDay() int64 {
	if !s.Enabled {
		return 0
	}
	return int64(s.MaxAbnormalPerDay)
}
