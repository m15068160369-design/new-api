package setting

// Waffo Pancake dual-channel configuration: CN (CNY, WeChat Pay) and US
// (USD, cards) are fully independent gateways. Each owns its credentials,
// product, unit price, channel ratio and minimum top-up — nothing is shared
// with the legacy single Pancake gateway or with each other. A channel is
// enabled once its MerchantID + PrivateKey + ProductID are populated.
//
// Pricing: payMoney = topupAmount × UnitPrice × groupRatio × amountDiscount
// × Ratio. UnitPrice is the long-term price per unit; Ratio is a per-channel
// multiplier (e.g. 0.9 for a promotion) defaulting to 1.
var (
	WaffoPancakeCNMerchantID string
	WaffoPancakeCNPrivateKey string
	WaffoPancakeCNStoreID    string
	WaffoPancakeCNProductID  string
	WaffoPancakeCNReturnURL  string
	WaffoPancakeCNUnitPrice  float64 = 1.0
	WaffoPancakeCNRatio      float64 = 1.0
	WaffoPancakeCNMinTopUp   int     = 1

	WaffoPancakeUSMerchantID string
	WaffoPancakeUSPrivateKey string
	WaffoPancakeUSStoreID    string
	WaffoPancakeUSProductID  string
	WaffoPancakeUSReturnURL  string
	WaffoPancakeUSUnitPrice  float64 = 1.0
	WaffoPancakeUSRatio      float64 = 1.0
	WaffoPancakeUSMinTopUp   int     = 1
)
