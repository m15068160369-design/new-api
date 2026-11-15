package model

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// 充值防滥用：按账号每日非成功订单限额（2026-11-15 拍板）。
//
// 规则：状态非 success（pending/failed/expired）都算异常、占用当日额度；success 不占。
// pending 全计入意味着「正在充值中」也占额度——从严口径，真人一天 5 笔非成功已属异常。
// 判定在建单前做纯查询（不写状态、无后台任务）；webhook 回执落库后该单自动转
// success，计数随之下降（回执迟到误扣的自愈路径）。

var ErrTopUpAbnormalLimit = errors.New("今日未完成支付的订单已达上限")

// topUpGuardLocks: 同一用户建单临界区串行化。
// 目的：把「计数检查+插入」锁成原子段，防并发请求在 check-insert 间隙内双双通过
// 计数导致超发。（单进程假设成立：本站单机单实例部署。）
var topUpGuardLocks sync.Map // map[int]*sync.Mutex

func topUpGuard(userId int) *sync.Mutex {
	v, _ := topUpGuardLocks.LoadOrStore(userId, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// CountTodayAbnormalTopUps 统计某用户今日（服务器本地时区自然日）创建的、当前状态非
// success 的充值订单数。与 checkin 相同的「今天」口径（time.Local 格式化日期）。
func CountTodayAbnormalTopUps(userId int) (int64, error) {
	dayStart := time.Now().Format("2006-01-02")
	startTs, err := time.ParseInLocation("2006-01-02", dayStart, time.Local)
	if err != nil {
		return 0, fmt.Errorf("解析当日零点失败: %w", err)
	}
	var count int64
	err = DB.Model(&TopUp{}).
		Where("user_id = ? AND create_time >= ? AND status != ?", userId, startTs.Unix(), common.TopUpStatusSuccess).
		Count(&count).Error
	return count, err
}

// CreateTopUpWithLimit 限额感知的充值订单创建入口，各支付渠道建单统一走这里。
// maxAbnormal <= 0 表示不启用限额（等价于直接 Insert）。
// 锁内完成「计数检查 + 插入」，同一用户并发建单不会在间隙绕过计数。
// 注意：检查在插入之前——被拒绝的请求不产生订单记录，避免拒绝本身污染计数造成假熔断。
// 计数查询出错时放行：若 DB 真有故障，下方 Create 会同样失败并报错，
// 不存在「DB 故障窗口内静默放行刷单」的路径。
func CreateTopUpWithLimit(topUp *TopUp, maxAbnormal int64) error {
	mu := topUpGuard(topUp.UserId)
	mu.Lock()
	defer mu.Unlock()

	if maxAbnormal > 0 {
		count, err := CountTodayAbnormalTopUps(topUp.UserId)
		if err == nil && count >= maxAbnormal {
			// "%w" 包装保留哨兵错误，controller 用 errors.Is 识别；文案对用户说明
			// 三件事：触发原因、明日零点自动恢复、已付款订单的自查路径。
			return fmt.Errorf("%w（每日 %d 单），已暂停创建新订单，明日零点自动恢复；已付款的订单请稍候刷新查看到账，如有疑问请联系管理员", ErrTopUpAbnormalLimit, maxAbnormal)
		}
	}
	return DB.Create(topUp).Error
}
