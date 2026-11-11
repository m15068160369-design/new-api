package controller

// Waffo Pancake dual-channel (CN/US) top-up flow. Each channel is a fully
// independent gateway: own credentials, own product, own unit price × channel
// ratio pricing and own minimum top-up. Both channels share the waffo_pancake
// payment *provider* — webhook dispatch, signature verification and
// RechargeWaffoPancake all resolve by trade_no + provider, so a channel needs
// no separate webhook handling; only the visible PaymentMethod type differs.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

// waffoPancakeChannel captures everything that differs between the CN (CNY)
// and US (USD) channels. Function fields keep the struct static while the
// underlying settings stay hot-reloadable through the option map.
type waffoPancakeChannel struct {
	methodType  string // visible PaymentMethod type
	currency    string // checkout pricing currency (ISO 4217)
	tradePrefix string // trade_no prefix for webhook-side channeling/logging
	displayName string
	isEnabled   func() bool
	merchantID  func() string
	privateKey  func() string
	productID   func() string
	unitPrice   func() float64
	ratio       func() float64
	minTopUp    func() int
}

var waffoPancakeCNChannel = waffoPancakeChannel{
	methodType:  model.PaymentMethodWaffoPancakeCN,
	currency:    "CNY",
	tradePrefix: "WAFFO_PANCAKE_CN-",
	displayName: "Waffo Pancake CN",
	isEnabled:   isWaffoPancakeCNTopUpEnabled,
	merchantID:  func() string { return setting.WaffoPancakeCNMerchantID },
	privateKey: func() string { return setting.WaffoPancakeCNPrivateKey },
	productID:  func() string { return setting.WaffoPancakeCNProductID },
	unitPrice:  func() float64 { return setting.WaffoPancakeCNUnitPrice },
	ratio:      func() float64 { return setting.WaffoPancakeCNRatio },
	minTopUp:   func() int { return setting.WaffoPancakeCNMinTopUp },
}

var waffoPancakeUSChannel = waffoPancakeChannel{
	methodType:  model.PaymentMethodWaffoPancakeUS,
	currency:    "USD",
	tradePrefix: "WAFFO_PANCAKE_US-",
	displayName: "Waffo Pancake US",
	isEnabled:   isWaffoPancakeUSTopUpEnabled,
	merchantID:  func() string { return setting.WaffoPancakeUSMerchantID },
	privateKey: func() string { return setting.WaffoPancakeUSPrivateKey },
	productID:  func() string { return setting.WaffoPancakeUSProductID },
	unitPrice:  func() float64 { return setting.WaffoPancakeUSUnitPrice },
	ratio:      func() float64 { return setting.WaffoPancakeUSRatio },
	minTopUp:   func() int { return setting.WaffoPancakeUSMinTopUp },
}

// getWaffoPancakeChannelPayMoney prices a top-up on the channel:
// amount × unitPrice × groupRatio × amountDiscount × channelRatio.
// A non-positive ratio degrades to 1 so a bad config cannot zero out checkout.
func getWaffoPancakeChannelPayMoney(ch waffoPancakeChannel, amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromFloat(common.QuotaPerUnit))
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok && ds > 0 {
		discount = ds
	}

	channelRatio := ch.ratio()
	if channelRatio <= 0 {
		channelRatio = 1
	}

	payMoney := dAmount.
		Mul(decimal.NewFromFloat(ch.unitPrice())).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount)).
		Mul(decimal.NewFromFloat(channelRatio))

	return payMoney.InexactFloat64()
}

func handleWaffoPancakeChannelAmount(c *gin.Context, ch waffoPancakeChannel) {
	if !ch.isEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": ch.displayName + " 渠道未启用"})
		return
	}

	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < int64(ch.minTopUp()) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", ch.minTopUp())})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getWaffoPancakeChannelPayMoney(ch, req.Amount, group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": fmt.Sprintf("%.2f", payMoney)})
}

func handleWaffoPancakeChannelPay(c *gin.Context, ch waffoPancakeChannel) {
	if !ch.isEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": ch.displayName + " 渠道未启用"})
		return
	}

	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < int64(ch.minTopUp()) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", ch.minTopUp())})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getWaffoPancakeChannelPayMoney(ch, req.Amount, group)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	tradeNo := fmt.Sprintf("%s%d-%d-%s", ch.tradePrefix, id, time.Now().UnixMilli(), randstr.String(6))
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          normalizeWaffoPancakeTopUpAmount(req.Amount),
		Money:           payMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   ch.methodType,
		PaymentProvider: model.PaymentProviderWaffoPancake,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("%s 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", ch.displayName, id, tradeNo, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	expiresInSeconds := 45 * 60
	session, err := service.CreateWaffoPancakeCheckoutSessionForCreds(c.Request.Context(), ch.merchantID(), ch.privateKey(), &service.WaffoPancakeCreateSessionParams{
		ProductID:     ch.productID(),
		Currency:      ch.currency,
		BuyerIdentity: getWaffoPancakeBuyerIdentity(user),
		PriceSnapshot: &service.WaffoPancakePriceSnapshot{
			Amount:      formatWaffoPancakeAmount(payMoney),
			TaxCategory: "saas",
		},
		BuyerEmail:              getWaffoPancakeBuyerEmail(user),
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: tradeNo,
	})
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("%s 创建结账会话失败 user_id=%d trade_no=%s error=%q", ch.displayName, id, tradeNo, err.Error()))
		topUp.Status = common.TopUpStatusFailed
		_ = topUp.Update()
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("%s 充值订单创建成功 user_id=%d trade_no=%s session_id=%s amount=%d money=%.2f", ch.displayName, id, tradeNo, session.SessionID, req.Amount, payMoney))

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url":     session.CheckoutURL,
			"session_id":      session.SessionID,
			"expires_at":      session.ExpiresAt,
			"order_id":        tradeNo,
			"token":           session.Token,
			"token_expires_at": session.TokenExpiresAt,
		},
	})
}

// Thin route handlers — one pair per channel.
func RequestWaffoPancakeCNAmount(c *gin.Context) { handleWaffoPancakeChannelAmount(c, waffoPancakeCNChannel) }
func RequestWaffoPancakeCNPay(c *gin.Context)    { handleWaffoPancakeChannelPay(c, waffoPancakeCNChannel) }
func RequestWaffoPancakeUSAmount(c *gin.Context) { handleWaffoPancakeChannelAmount(c, waffoPancakeUSChannel) }
func RequestWaffoPancakeUSPay(c *gin.Context)    { handleWaffoPancakeChannelPay(c, waffoPancakeUSChannel) }
