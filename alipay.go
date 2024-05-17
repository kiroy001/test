package carrier

import (
	"context"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
	"github.com/go-pay/gopay/pkg/xlog"
)

// Alipay 支付创建
func Alipay(out_trade_no string, price string) string {
	client, err := alipay.NewClient(appid, alikey, true)
	if err != nil {
		xlog.Error(err)
		return ""
	}

	client.DebugSwitch = gopay.DebugOn

	client.SetLocation(alipay.LocationShanghai).
		SetCharset(alipay.UTF8).
		SetSignType(alipay.RSA2).
		SetReturnUrl("https://www.fmm.ink").
		SetNotifyUrl("https://www.fmm.ink")

	bm := make(gopay.BodyMap)
	bm.Set("subject", "手机网站支付").
		Set("out_trade_no", out_trade_no).
		Set("total_amount", price).
		Set("timeout_express", "2m")

	aliRsp, err := client.TradeWapPay(context.Background(), bm)
	if err != nil {
		if bizErr, ok := alipay.IsBizError(err); ok {
			xlog.Errorf("%+v", bizErr)
			// do something
			return "11111"
		}
		xlog.Errorf("client.TradePay(%+v),err:%+v", bm, err)
		return "22222"
	}
	return aliRsp
}

type TradeQueryResponse struct {
	Response     *TradeQuery `json:"alipay_trade_query_response"`
	AlipayCertSn string      `json:"alipay_cert_sn,omitempty"`
	SignData     string      `json:"-"`
	Sign         string      `json:"sign"`
}
type ErrorResponse struct {
	Code    string `json:"code"`
	Msg     string `json:"msg"`
	SubCode string `json:"sub_code,omitempty"`
	SubMsg  string `json:"sub_msg,omitempty"`
}

type TradeFundBill struct {
	FundChannel string `json:"fund_channel,omitempty"` // 同步通知里是 fund_channel
	Amount      string `json:"amount,omitempty"`
	RealAmount  string `json:"real_amount,omitempty"`
	FundType    string `json:"fund_type,omitempty"`
}

type HbFqPayInfo struct {
	UserInstallNum string `json:"user_install_num,omitempty"`
}

type TradeSettleDetail struct {
	OperationType     string `json:"operation_type,omitempty"`
	OperationSerialNo string `json:"operation_serial_no,omitempty"`
	OperationDt       string `json:"operation_dt,omitempty"`
	TransOut          string `json:"trans_out,omitempty"`
	TransIn           string `json:"trans_in,omitempty"`
	Amount            string `json:"amount,omitempty"`
	OriTransOut       string `json:"ori_trans_out,omitempty"`
	OriTransIn        string `json:"ori_trans_in,omitempty"`
}

type TradeSettleInfo struct {
	TradeSettleDetailList []*TradeSettleDetail `json:"trade_settle_detail_list,omitempty"`
}

type BkAgentRespInfo struct {
	BindtrxId        string `json:"bindtrx_id,omitempty"`
	BindclrissrId    string `json:"bindclrissr_id,omitempty"`
	BindpyeracctbkId string `json:"bindpyeracctbk_id,omitempty"`
	BkpyeruserCode   string `json:"bkpyeruser_code,omitempty"`
	EstterLocation   string `json:"estter_location,omitempty"`
}

type ChargeInfo struct {
	ChargeFee               string    `json:"charge_fee,omitempty"`
	OriginalChargeFee       string    `json:"original_charge_fee,omitempty"`
	SwitchFeeRate           string    `json:"switch_fee_rate,omitempty"`
	IsRatingOnTradeReceiver string    `json:"is_rating_on_trade_receiver,omitempty"`
	IsRatingOnSwitch        string    `json:"is_rating_on_switch,omitempty"`
	ChargeType              string    `json:"charge_type,omitempty"`
	SubFeeDetailList        []*SubFee `json:"sub_fee_detail_list,omitempty"`
}

type SubFee struct {
	ChargeFee         string `json:"charge_fee,omitempty"`
	OriginalChargeFee string `json:"original_charge_fee,omitempty"`
	SwitchFeeRate     string `json:"switch_fee_rate,omitempty"`
}

type TradeQuery struct {
	ErrorResponse
	TradeNo               string           `json:"trade_no,omitempty"`
	OutTradeNo            string           `json:"out_trade_no,omitempty"`
	BuyerLogonId          string           `json:"buyer_logon_id,omitempty"`
	TradeStatus           string           `json:"trade_status,omitempty"`
	TotalAmount           string           `json:"total_amount,omitempty"`
	TransCurrency         string           `json:"trans_currency,omitempty"`
	SettleCurrency        string           `json:"settle_currency,omitempty"`
	SettleAmount          string           `json:"settle_amount,omitempty"`
	PayCurrency           string           `json:"pay_currency,omitempty"`
	PayAmount             string           `json:"pay_amount,omitempty"`
	SettleTransRate       string           `json:"settle_trans_rate,omitempty"`
	TransPayRate          string           `json:"trans_pay_rate,omitempty"`
	BuyerPayAmount        string           `json:"buyer_pay_amount,omitempty"`
	PointAmount           string           `json:"point_amount,omitempty"`
	InvoiceAmount         string           `json:"invoice_amount,omitempty"`
	SendPayDate           string           `json:"send_pay_date,omitempty"`
	ReceiptAmount         string           `json:"receipt_amount,omitempty"`
	StoreId               string           `json:"store_id,omitempty"`
	TerminalId            string           `json:"terminal_id,omitempty"`
	FundBillList          []*TradeFundBill `json:"fund_bill_list"`
	StoreName             string           `json:"store_name,omitempty"`
	BuyerUserId           string           `json:"buyer_user_id,omitempty"`
	BuyerOpenId           string           `json:"buyer_open_id,omitempty"`
	DiscountGoodsDetail   string           `json:"discount_goods_detail,omitempty"`
	IndustrySepcDetail    string           `json:"industry_sepc_detail,omitempty"`
	IndustrySepcDetailGov string           `json:"industry_sepc_detail_gov,omitempty"`
	IndustrySepcDetailAcc string           `json:"industry_sepc_detail_acc,omitempty"`
	ChargeAmount          string           `json:"charge_amount,omitempty"`
	ChargeFlags           string           `json:"charge_flags,omitempty"`
	SettlementId          string           `json:"settlement_id,omitempty"`
	TradeSettleInfo       *TradeSettleInfo `json:"trade_settle_info,omitempty"`
	AuthTradePayMode      string           `json:"auth_trade_pay_mode,omitempty"`
	BuyerUserType         string           `json:"buyer_user_type,omitempty"`
	MdiscountAmount       string           `json:"mdiscount_amount,omitempty"`
	DiscountAmount        string           `json:"discount_amount,omitempty"`
	Subject               string           `json:"subject,omitempty"`
	Body                  string           `json:"body,omitempty"`
	AlipaySubMerchantId   string           `json:"alipay_sub_merchant_id,omitempty"`
	ExtInfos              string           `json:"ext_infos,omitempty"`
	PassbackParams        string           `json:"passback_params,omitempty"`
	HbFqPayInfo           *HbFqPayInfo     `json:"hb_fq_pay_info,omitempty"`
	CreditPayMode         string           `json:"credit_pay_mode,omitempty"`
	CreditBizOrderId      string           `json:"credit_biz_order_id,omitempty"`
	HybAmount             string           `json:"hyb_amount,omitempty"`
	BkagentRespInfo       *BkAgentRespInfo `json:"bkagent_resp_info,omitempty"`
	ChargeInfoList        []*ChargeInfo    `json:"charge_info_list,omitempty"`
	BizSettleMode         string           `json:"biz_settle_mode,omitempty"`
}

// client.TradeQuery()
func TradeSQuery(out_trade_no string) (resp *alipay.TradeQueryResponse) {
	client, err := alipay.NewClient(appid, alikey, true)
	if err != nil {
		xlog.Error(err)
	}
	//配置公共参数
	client.SetCharset("utf-8").
		SetSignType(alipay.RSA2)
	//SetAppAuthToken("201908BB03f542de8ecc42b985900f5080407abc")

	//请求参数
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", out_trade_no)

	//查询订单
	aliRsp, errs := client.TradeQuery(context.Background(), bm)
	if err != nil {
		xlog.Error("err:", errs)
	}
	return aliRsp
}
