// WebSocket 内容审核网关。同步模式先审核再转发；异步模式仅观察。
// 支持 OpenIM gzip + Gob GeneralWsReq + protobuf MsgData，以及 JSON 请求。
// 解码只用于审核，放行时仍转发原始帧。Go 1.18+。
// 同步模式解析失败/审核失败均拦截；off 或 moderation-async=true 不拦截。
//间接拦截转发openim的阿里云审核网关,利用阿里云审核
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// ============================================================================
// 第一部分：默认配置（封装交付时的"填写"入口）
//
// 优先级：命令行参数  >  环境变量  >  本节常量  >  内置默认
//
// 你可以直接把密钥填在下面 DefaultXxx 常量里，然后编译成一个 exe 交给使用者；
// 也可以保留为空，让使用者用环境变量或命令行传入（更安全，推荐生产使用）。
//
// ⚠️ 如果在这里硬编码了真实密钥：
//   1. 不要把本文件提交到公开仓库；
//   2. 建议编译后只交付 exe，不交付源码。
// ============================================================================

const (
	// ---------- 阿里云内容安全（绿网 CIP）凭证 ----------
	// 在 https://ram.console.aliyun.com/manage/ak 创建 AccessKey
	DefaultAccessKeyID     = "" // 形如 LTAI5txxxxxxxxxxxxxx
	DefaultAccessKeySecret = "" // 32 位随机串

	// 内容安全所在地域，需与你在控制台购买绿网服务的地域一致
	DefaultRegion = "cn-shanghai"

	// 绿网 CIP 网关，留空自动推导为 green-cip.<region>.aliyuncs.com
	DefaultEndpoint = ""

	// 文本检测服务名: chat_detection / comment_detection / nickname_detection
	DefaultTextService = ServiceChatDetection
	// 图片检测服务名: baselineCheck
	DefaultImageService = ServiceImageBaselineCheck

	// ---------- 服务本身 ----------
	DefaultAddr     = ":8787"
	DefaultUpstream = "ws://????:10001"

	// 违规检验模式: local | aliyun | off
	DefaultModeration = "aliyun"

	// 是否异步审核。
	//   false(默认)：同步阻塞——审核通过才转发到 OpenIM，命中违规直接丢弃；
	//   true        ：异步旁路——先转发再审核，只记日志，绝不阻断。
	DefaultModerationAsync = false

	// 命中违规后的回调 URL（可选，留空不回调）
	DefaultViolationCallback = ""

	// 是否打印详细的请求/响应日志（排查阿里云是否被真实调用时打开）。
	//   true(默认)：打印送检文本、阿里云请求 URL/body、HTTP 状态码、响应体、耗时；
	//   false       ：只打印命中/放行结论。
	DefaultDebug = true
)

var (
	DefaultDialTimeout  = 10 * time.Second
	DefaultCheckTimeout = 3 * time.Second
)

// envOr 返回环境变量 key 的值（先查带前缀的 WEF_<key>，再查裸 key）；
// 都为空时返回 fallback。
func envOr(key, fallback string) string {
	for _, k := range []string{"WEF_" + key, key} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return fallback
}

// envDurationOr 读取时长型环境变量（如 "5s"），解析失败或为空时返回 fallback。
func envDurationOr(key string, fallback time.Duration) time.Duration {
	v := envOr(key, "")
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

// Config 是代理的全部配置。
type Config struct {
	Addr              string // 本地监听地址（命令行程序使用）
	Upstream          string // OpenIM msggateway 的 ws 地址
	Moderation        string // local | aliyun | off
	AppKeyID          string // 阿里云 AccessKeyId
	AppKeySecret      string // 阿里云 AccessKeySecret
	Region            string // 阿里云 region
	Endpoint          string // 阿里云绿网网关，留空自动推导
	TextService       string // 文本检测 service，默认 chat_detection
	ImageService      string // 图片检测 service，默认 baselineCheck
	ViolationCallback string // 命中违规后回调通知的 URL（可选）
	ModerationAsync   bool   // false=同步拦截(默认) true=异步旁路
	Debug             bool   // true=打印详细请求/响应日志
	DialTimeout       time.Duration
	CheckTimeout      time.Duration
}

// envBoolOr 读取布尔型环境变量（1/true/yes/on 为真），
// 未设置或无法识别时返回 fallback。
func envBoolOr(key string, fallback bool) bool {
	v := strings.ToLower(envOr(key, ""))
	switch v {
	case "":
		return fallback
	case "1", "true", "yes", "on", "y":
		return true
	case "0", "false", "no", "off", "n":
		return false
	default:
		return fallback
	}
}

// newDefaultConfig 构造一份"默认配置"：以代码内常量为准，
// 再用环境变量覆盖（环境变量对使用者更安全，不必写进命令行历史）。
func newDefaultConfig() Config {
	return Config{
		Addr:              envOr("ADDR", DefaultAddr),
		Upstream:          envOr("UPSTREAM", DefaultUpstream),
		Moderation:        envOr("MODERATION", DefaultModeration),
		ModerationAsync:   envBoolOr("MODERATION_ASYNC", DefaultModerationAsync),
		AppKeyID:          envOr("ALIYUN_AK", DefaultAccessKeyID),
		AppKeySecret:      envOr("ALIYUN_SK", DefaultAccessKeySecret),
		Region:            envOr("ALIYUN_REGION", DefaultRegion),
		Endpoint:          envOr("ALIYUN_ENDPOINT", DefaultEndpoint),
		TextService:       envOr("ALIYUN_TEXT_SERVICE", DefaultTextService),
		ImageService:      envOr("ALIYUN_IMAGE_SERVICE", DefaultImageService),
		ViolationCallback: envOr("CALLBACK", DefaultViolationCallback),
		Debug:             envBoolOr("DEBUG", DefaultDebug),
		DialTimeout:       envDurationOr("DIAL_TIMEOUT", DefaultDialTimeout),
		CheckTimeout:      envDurationOr("CHECK_TIMEOUT", DefaultCheckTimeout),
	}
}

// applyDefaults 用内置默认补全零值字段（供库调用方使用）。
func (c *Config) applyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.Upstream == "" {
		c.Upstream = DefaultUpstream
	}
	if c.Moderation == "" {
		c.Moderation = DefaultModeration
	}
	if c.Region == "" {
		c.Region = DefaultRegion
	}
	if c.TextService == "" {
		c.TextService = DefaultTextService
	}
	if c.ImageService == "" {
		c.ImageService = DefaultImageService
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = DefaultCheckTimeout
	}
}

// ============================================================================
// 第二部分：违规词检验模块
//
// 提供两种实现，运行时可切换（-moderation 参数）：
//   1. localModerator   : 本地敏感词表兜底，零依赖，随时可用
//   2. aliyunModerator  : 阿里云内容安全（绿网 CIP）OpenAPI，手写 HMAC 签名直连，
//                         不引入任何第三方 SDK，只要有 AccessKey 就能用
//
// 对外统一接口 Moderator.Check(ctx, text) -> Result
// ============================================================================

// ModerationResult 检验结果。
type ModerationResult struct {
	Violation bool     // 是否命中违规
	Label     string   // 违规标签，如 "porn" / "ad" / "keyword"，正常为空
	Reason    string   // 命中原因 / 命中词
	Raw       string   // 原始返回，便于排查
	Words     []string // 命中的敏感词（本地模式使用）
}

// Moderator 违规词检验器接口。
type Moderator interface {
	Check(ctx context.Context, text string) (ModerationResult, error)
	Name() string
}

// ImageChecker 可选接口：支持图片审核的检验器额外实现此接口。
// inspectOutgoing 会在发现图片 URL 时调用（仅当检验器实现了该接口）。
type ImageChecker interface {
	CheckImage(ctx context.Context, imageURL string) (ModerationResult, error)
}

// ----------------------------------------------------------------------------
// 2.1) 本地敏感词兜底实现
// ----------------------------------------------------------------------------

// localModerator 基于内存敏感词表做简单包含匹配，用于离线 / 无 AK 时兜底。
type localModerator struct {
	words []string
}

// newLocalModerator 用内置默认词表构造（可按需替换 defaultBadWords）。
func newLocalModerator() *localModerator {
	return &localModerator{words: defaultBadWords}
}

func (m *localModerator) Name() string { return "local" }

func (m *localModerator) Check(_ context.Context, text string) (ModerationResult, error) {
	if strings.TrimSpace(text) == "" {
		return ModerationResult{}, nil
	}
	lower := strings.ToLower(text)
	var hit []string
	for _, w := range m.words {
		if w == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(w)) {
			hit = append(hit, w)
		}
	}
	if len(hit) == 0 {
		return ModerationResult{}, nil
	}
	return ModerationResult{
		Violation: true,
		Label:     "keyword",
		Reason:    "命中本地敏感词: " + strings.Join(hit, ","),
		Words:     hit,
	}, nil
}

// defaultBadWords 仅为示例词表，请按实际业务补充/替换。
var defaultBadWords = []string{
	"赌博", "博彩", "洗钱", "诈骗", "代开发票", "枪支", "毒品", "色情", "裸聊", "招嫖",
}

// ----------------------------------------------------------------------------
// 2.2) 阿里云内容安全（绿网 CIP）实现
//
// 严格对齐官方 alibabacloud-green20220302 SDK 的 wire 格式：
//   endpoint  : green-cip.<region>.aliyuncs.com
//   version   : 2022-03-02
//   action    : TextModeration / ImageModeration
//   method    : POST，RPC 风格
//   query 公共参数 : Action/Format=json/Version/Timestamp/SignatureNonce/
//                    SignatureMethod=HMAC-SHA1/SignatureVersion=1.0/
//                    AccessKeyId/Signature
//   body  业务参数 : Service / ServiceParameters  (application/x-www-form-urlencoded)
//   签名   : 把 query 与 body 参数合并后按 key 排序做 url encode，
//            再计算 POST&%2F&<encoded> 的 HMAC-SHA1(secret+"&")
//
// 该格式与 darabonba-openapi 的 DoRPCRequest + openapi-util.GetRPCSignature 完全一致。
// ----------------------------------------------------------------------------

// 业务服务类型常量（对应控制台购买的服务名）
const (
	// ServiceChatDetection 聊天/私聊文本检测，对应 Python 示例的默认值
	ServiceChatDetection = "chat_detection"
	// ServiceCommentDetection 评论/弹幕文本检测
	ServiceCommentDetection = "comment_detection"
	// ServiceNicknameDetection 昵称检测
	ServiceNicknameDetection = "nickname_detection"
	// ServiceImageBaselineCheck 通用图片基线检测
	ServiceImageBaselineCheck = "baselineCheck"
)

type aliyunModerator struct {
	accessKeyID     string
	accessKeySecret string
	region          string
	endpoint        string
	textService     string // 文本检测服务名，默认 chat_detection
	imageService    string // 图片检测服务名，默认 baselineCheck
	debug           bool   // true=打印请求/响应日志
	httpClient      *http.Client
}

// newAliyunModerator 构造阿里云内容安全检验器。
// region 例如 "cn-shanghai"；endpoint 留空则自动用 green-cip.<region>.aliyuncs.com。
func newAliyunModerator(ak, sk, region, endpoint string) *aliyunModerator {
	if region == "" {
		region = DefaultRegion
	}
	if endpoint == "" {
		endpoint = "green-cip." + region + ".aliyuncs.com"
	}
	return &aliyunModerator{
		accessKeyID:     ak,
		accessKeySecret: sk,
		region:          region,
		endpoint:        endpoint,
		textService:     ServiceChatDetection,
		imageService:    ServiceImageBaselineCheck,
		httpClient:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (m *aliyunModerator) Name() string { return "aliyun" }

// aliyunTextData 解析 TextModeration 的 Data 字段。
type aliyunTextData struct {
	// labels 为逗号分隔字符串，如 "porn,ad"
	Labels string `json:"labels"`
	Reason string `json:"reason"`
	Result []struct {
		Label       string  `json:"label"`
		Confidence  float64 `json:"confidence"`
		Description string  `json:"description"`
		RiskWords   string  `json:"riskWords"`
	} `json:"result"`
}

// Check 调用阿里云绿网 TextModeration 接口（文本）。
func (m *aliyunModerator) Check(ctx context.Context, text string) (ModerationResult, error) {
	if strings.TrimSpace(text) == "" {
		return ModerationResult{}, nil
	}

	// 业务参数：与 Python 示例一致，ServiceParameters 是一个 JSON 字符串
	serviceParams, _ := json.Marshal(map[string]any{
		"content": text,
		"dataId":  newUUID(),
	})

	body := map[string]string{
		"Service":           m.textService,
		"ServiceParameters": string(serviceParams),
	}

	respBody, err := m.doRPC(ctx, "TextModeration", "2022-03-02", body)
	if err != nil {
		return ModerationResult{}, err
	}
	raw := string(respBody)

	var parsed struct {
		Code    int             `json:"Code"`
		Message string          `json:"Message"`
		Data    json.RawMessage `json:"Data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ModerationResult{Raw: raw}, fmt.Errorf("解析阿里云响应失败: %w, raw=%s", err, raw)
	}
	if parsed.Code != 200 {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云返回错误: code=%d msg=%s", parsed.Code, parsed.Message)
	}

	// Data.labels 为逗号分隔字符串；Data.result 为明细数组
	var data aliyunTextData
	if len(parsed.Data) == 0 || string(parsed.Data) == "null" {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云 Data 为空")
	}
	if err := json.Unmarshal(parsed.Data, &data); err != nil {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云 Data 解析失败: %w", err)
	}

	labels := splitLabels(data.Labels)
	for _, r := range data.Result {
		if r.Label != "" {
			labels = append(labels, r.Label)
		}
	}
	labels = uniqueNonEmpty(labels)

	// 过滤掉表示"正常"的标签
	riskLabels := filterRiskLabels(labels)
	if len(riskLabels) == 0 {
		return ModerationResult{Raw: raw}, nil
	}

	return ModerationResult{
		Violation: true,
		Label:     strings.Join(riskLabels, ","),
		Reason:    firstNonEmpty(data.Reason, "阿里云命中: "+strings.Join(riskLabels, ",")),
		Raw:       raw,
	}, nil
}

// buildStringToSign 构造待签名串：METHOD&%2F&urlEncode(排序后的参数串)。
func (m *aliyunModerator) buildStringToSign(signedParams map[string]string, method string) string {
	values := url.Values{}
	for k, v := range signedParams {
		values.Set(k, v)
	}
	canonical := values.Encode()
	canonical = strings.ReplaceAll(canonical, "+", "%20")
	canonical = strings.ReplaceAll(canonical, "*", "%2A")
	canonical = strings.ReplaceAll(canonical, "%7E", "~")
	return method + "&%2F&" + percentEncode(canonical)
}

// signRPC 复刻官方 SDK 的 RPC 签名（openapi-util.GetRPCSignature）。
// signedParams 必须包含 query 与 body 的全部参数（不含 Signature 本身）。
func (m *aliyunModerator) signRPC(signedParams map[string]string, method string) string {
	stringToSign := m.buildStringToSign(signedParams, method)

	// HMAC-SHA1，密钥为 accessKeySecret + "&"
	h := hmac.New(sha1.New, []byte(m.accessKeySecret+"&"))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// percentEncode 按阿里云要求做 URL 编码（RFC3986 风格）。
func percentEncode(s string) string {
	encoded := url.QueryEscape(s)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}

// endpointURL 返回完整的 base URL。
// 若 endpoint 已带 http:// 或 https:// 前缀则直接使用（便于测试/私网网关），
// 否则补默认的 https:// 前缀。
func (m *aliyunModerator) endpointURL() string {
	ep := m.endpoint
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		return strings.TrimSuffix(ep, "/")
	}
	return "https://" + strings.TrimSuffix(ep, "/")
}

// doRPC 发起一次 RPC 风格 OpenAPI 调用。
// body 为业务参数（会与 query 一起参与签名，并以 form 表单形式提交）。
func (m *aliyunModerator) doRPC(ctx context.Context, action, version string, body map[string]string) ([]byte, error) {
	// 公共 query 参数（对齐 darabonba-openapi DoRPCRequest）
	query := map[string]string{
		"Action":           action,
		"Format":           "json",
		"Version":          version,
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"SignatureNonce":   newUUID(),
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"AccessKeyId":      m.accessKeyID,
	}

	// 签名参数 = query ∪ body（官方 SDK: tea.Merge(request_.Query, body)）
	signedParams := make(map[string]string, len(query)+len(body))
	for k, v := range query {
		signedParams[k] = v
	}
	for k, v := range body {
		signedParams[k] = v
	}
	query["Signature"] = m.signRPC(signedParams, http.MethodPost)

	// 组装 URL
	q := url.Values{}
	for k, v := range query {
		q.Set(k, v)
	}
	fullURL := m.endpointURL() + "/?" + q.Encode()

	// 组装 body（application/x-www-form-urlencoded）
	form := url.Values{}
	for k, v := range body {
		form.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-acs-action", action)
	req.Header.Set("x-acs-version", version)

	if m.debug {
		log.Printf("[阿里云->请求] action=%s url=%s body=%s",
			action, redactURL(fullURL), form.Encode())
	}

	start := time.Now()
	resp, err := m.httpClient.Do(req)
	cost := time.Since(start)
	if err != nil {
		log.Printf("[阿里云->失败] action=%s cost=%s err=%v", action, cost, err)
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[阿里云->读取失败] action=%s cost=%s status=%d err=%v",
			action, cost, resp.StatusCode, err)
		return nil, err
	}

	if m.debug {
		log.Printf("[阿里云<-响应] action=%s status=%d cost=%s body=%s",
			action, resp.StatusCode, cost, truncate(string(respBytes), 2000))
	}
	if resp.StatusCode != http.StatusOK {
		return respBytes, fmt.Errorf("阿里云 HTTP 状态码异常: status=%d body=%s",
			resp.StatusCode, truncate(string(respBytes), 500))
	}

	return respBytes, nil
}

// ----------------------------------------------------------------------------
// 2.3) 阿里云图片审核
// ----------------------------------------------------------------------------

// CheckImage 调用阿里云绿网 ImageModeration 接口（图片 URL）。
// 对应 Python 示例中的 check_image，返回 suggestion: pass / block。
func (m *aliyunModerator) CheckImage(ctx context.Context, imageURL string) (ModerationResult, error) {
	if strings.TrimSpace(imageURL) == "" {
		return ModerationResult{}, nil
	}

	serviceParams, _ := json.Marshal(map[string]any{
		"imageUrl": imageURL,
		"dataId":   newUUID(),
	})
	body := map[string]string{
		"Service":           m.imageService,
		"ServiceParameters": string(serviceParams),
	}

	respBody, err := m.doRPC(ctx, "ImageModeration", "2022-03-02", body)
	if err != nil {
		return ModerationResult{}, err
	}
	raw := string(respBody)

	var parsed struct {
		Code    int             `json:"Code"`
		Message string          `json:"Message"`
		Data    json.RawMessage `json:"Data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ModerationResult{Raw: raw}, fmt.Errorf("解析阿里云响应失败: %w, raw=%s", err, raw)
	}
	if parsed.Code != 200 {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云返回错误: code=%d msg=%s", parsed.Code, parsed.Message)
	}

	var data struct {
		Result []struct {
			Label       string  `json:"label"`
			Confidence  float64 `json:"confidence"`
			Description string  `json:"description"`
		} `json:"result"`
	}
	if len(parsed.Data) == 0 || string(parsed.Data) == "null" {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云 Data 为空")
	}
	if err := json.Unmarshal(parsed.Data, &data); err != nil {
		return ModerationResult{Raw: raw}, fmt.Errorf("阿里云 Data 解析失败: %w", err)
	}

	var labels, reasons []string
	for _, r := range data.Result {
		if r.Label == "" {
			continue
		}
		labels = append(labels, r.Label)
		if r.Description != "" {
			reasons = append(reasons, r.Description)
		}
	}
	riskLabels := filterRiskLabels(uniqueNonEmpty(labels))
	if len(riskLabels) == 0 {
		return ModerationResult{Raw: raw}, nil
	}
	return ModerationResult{
		Violation: true,
		Label:     strings.Join(riskLabels, ","),
		Reason:    firstNonEmpty(strings.Join(reasons, "; "), "阿里云图片命中: "+strings.Join(riskLabels, ",")),
		Raw:       raw,
	}, nil
}

// ----------------------------------------------------------------------------
// 2.4) 小工具
// ----------------------------------------------------------------------------

// splitLabels 将 "porn,ad" 拆成 ["porn","ad"]。
func splitLabels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// uniqueNonEmpty 去重（保留顺序）并剔除空串。
func uniqueNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// normalLabels 表示"正常"的标签集合。
var normalLabels = map[string]struct{}{
	"nonlabel": {}, "normal": {}, "pass": {}, "none": {},
}

// filterRiskLabels 过滤掉正常标签，只保留风险标签。
func filterRiskLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if _, ok := normalLabels[strings.ToLower(strings.TrimSpace(l))]; ok {
			continue
		}
		out = append(out, l)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// newUUID 生成一个随机 UUID（v4 风格），用作 SignatureNonce / dataId。
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildModerator 按配置构造检验器，mode=off 返回 nil。
func buildModerator(c Config) Moderator {
	switch strings.ToLower(c.Moderation) {
	case "off", "none", "disable", "disabled":
		return nil
	case "aliyun":
		if c.AppKeyID == "" || c.AppKeySecret == "" {
			log.Println("警告: 选择了 aliyun 但未提供 AccessKey，自动降级为本地词表")
			return newLocalModerator()
		}
		m := newAliyunModerator(c.AppKeyID, c.AppKeySecret, c.Region, c.Endpoint)
		m.debug = c.Debug
		if c.TextService != "" {
			m.textService = c.TextService
		}
		if c.ImageService != "" {
			m.imageService = c.ImageService
		}
		return m
	default: // local
		return newLocalModerator()
	}
}

// logResult 统一打印检验结果，命中违规时重点输出。
// debug=true 时额外打印阿里云原始返回（res.Raw），便于核对是否真实调用了接口。
func logResult(debug bool, modName, sendID, recvID, text string, res ModerationResult) {
	if res.Violation {
		log.Printf("[命中违规] moderator=%s sendID=%s recvID=%s label=%s reason=%s text=%q",
			modName, sendID, recvID, res.Label, res.Reason, truncate(text, 200))
	} else {
		log.Printf("[检验通过] moderator=%s sendID=%s recvID=%s text=%q",
			modName, sendID, recvID, truncate(text, 100))
	}
	if debug && res.Raw != "" {
		log.Printf("[检验原始返回] moderator=%s raw=%s", modName, truncate(res.Raw, 1000))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// ============================================================================
// 第三部分：旁路检验（从 WS 帧中提取文本/图片并送检）
//
// 设计要点：
//   - 只在 App -> OpenIM 方向生效；
//   - 同步模式(默认)：审核通过才转发，命中违规则丢弃（不送达 OpenIM）；
//   - 异步模式(ModerationAsync=true)：审核失败/超时一律放行，原样转发，绝不阻断。
//   - 文本与图片都送检（图片仅当检验器实现 ImageChecker 接口时）。
// ============================================================================

// openIMMsg 是 OpenIM 消息体的最小子集，用于提取发送者/接收者/内容。
type openIMMsg struct {
	SendID      string          `json:"sendID"`
	RecvID      string          `json:"recvID"`
	GroupID     string          `json:"groupID"`
	ContentType int             `json:"contentType"`
	Content     json.RawMessage `json:"content"`
}

// inspectOutgoing 对 App 发往 OpenIM 的消息做检验。
//
// 返回值 blocked 表示"是否拦截"（不转发给 OpenIM）：
//   - 同步模式(moderationAsync=false)：命中违规 -> blocked=true；
//     审核报错/超时或解析失败 -> blocked=true（不把未审核内容视为通过）。
//   - 异步模式(moderationAsync=true)：始终 blocked=false，只记日志。
//
// 注意：本函数在同步模式下会阻塞当前转发协程，直到审核返回或超时，
// 因此转发循环会被串行化（一条消息审核完才读下一条）。
func inspectOutgoing(m Moderator, checkTimeout time.Duration, moderationAsync, debug bool, userID string, data []byte) (blocked bool) {
	trace := frameTrace(data)
	msg, skip, format, err := parseOutgoing(data)
	if err != nil {
		if m == nil {
			log.Printf("[未送审] trace=%s reason=审核关闭且帧无法解析", trace)
			return false
		}
		log.Printf("[解析失败] blocked=%t format=%s frameLen=%d err=%v", !moderationAsync, format, len(data), err)
		return !moderationAsync
	}
	if skip {
		if debug {
			log.Printf("[协议直通] format=%s 非发送消息请求，不送审", format)
		}
		return false
	}
	if msg.SendID == "" {
		msg.SendID = userID
	}
	text := extractTextContent(msg.Content)
	logMessageBody("发送消息", trace, userID, msg)
	if m == nil {
		log.Printf("[未送审] trace=%s aliyun=否 reason=审核关闭", trace)
		return false
	}
	if !validText(text) {
		log.Printf("[正文非法] blocked=%t contentType=%d", !moderationAsync, msg.ContentType)
		return !moderationAsync
	}
	type checkTask struct{ kind, value string }
	var tasks []checkTask
	if strings.TrimSpace(text) != "" {
		tasks = append(tasks, checkTask{"text", text})
	}
	if _, ok := m.(ImageChecker); ok {
		for _, u := range extractImageURLs(msg.Content, msg.ContentType == 102) {
			tasks = append(tasks, checkTask{"image", u})
		}
	}
	if len(tasks) == 0 {
		log.Printf("[未送审] trace=%s aliyun=否 reason=未提取到支持的审核项 contentType=%d", trace, msg.ContentType)
		// 明确的文本类型或图片类型提取失败，不能假装已审核。
		required := msg.ContentType == 0 || msg.ContentType == 101 || msg.ContentType == 106 || msg.ContentType == 114
		if _, ok := m.(ImageChecker); ok && msg.ContentType == 102 {
			required = true
		}
		if required {
			log.Printf("[正文提取失败] blocked=%t format=%s contentType=%d sendID=%s", !moderationAsync, format, msg.ContentType, msg.SendID)
			return !moderationAsync
		}
		if debug {
			log.Printf("[跳过审核] contentType=%d 无支持的文本/图片内容（语音、视频等不在本文件审核范围）", msg.ContentType)
		}
		return false
	}
	if debug {
		log.Printf("[消息解码] format=%s contentType=%d sendID=%s recvID=%s tasks=%d", format, msg.ContentType, msg.SendID, msg.RecvID, len(tasks))
	}
	target := firstNonEmpty(msg.RecvID, msg.GroupID)

	// 单个任务的实际送检逻辑
	runOne := func(t checkTask) bool {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()

		if debug {
			log.Printf("[开始审核] moderator=%s kind=%s sendID=%s recvID=%s timeout=%s content=%q",
				m.Name(), t.kind, msg.SendID, target, checkTimeout, truncate(t.value, 300))
		}

		aliyun := "否"
		if m.Name() == "aliyun" {
			aliyun = "是"
		}
		log.Printf("[审核调用] trace=%s moderator=%s aliyun=%s kind=%s async=%t content=%q",
			trace, m.Name(), aliyun, t.kind, moderationAsync, truncate(t.value, 4096))
		start := time.Now()
		var res ModerationResult
		var err error
		if t.kind == "image" {
			imgChecker := m.(ImageChecker)
			res, err = imgChecker.CheckImage(ctx, t.value)
		} else {
			res, err = m.Check(ctx, t.value)
		}
		if err != nil {
			log.Printf("[审核结果] trace=%s moderator=%s result=失败 blocked=%t cost=%s err=%v", trace, m.Name(), !moderationAsync, time.Since(start), err)
			return !moderationAsync
		}
		verdict := "通过"
		if res.Violation {
			verdict = "违规"
		}
		log.Printf("[审核结果] trace=%s moderator=%s result=%s blocked=%t cost=%s label=%q reason=%q raw=%q",
			trace, m.Name(), verdict, res.Violation && !moderationAsync, time.Since(start), res.Label, res.Reason, truncate(res.Raw, 4096))
		logText := t.value
		if t.kind == "image" {
			logText = "[image]" + t.value
		}
		logResult(debug, m.Name(), msg.SendID, target, logText, res)
		return res.Violation
	}

	if moderationAsync {
		// 异步旁路：不阻断转发，只记录
		for _, t := range tasks {
			t := t
			go runOne(t)
		}
		return false
	}

	// 同步：任一命中即拦截
	for _, t := range tasks {
		if runOne(t) {
			log.Printf("[消息拦截] moderator=%s sendID=%s target=%s 该消息未转发到 OpenIM",
				m.Name(), msg.SendID, target)
			return true
		}
	}
	return false
}

// 单个原始帧/解压结果上限。gzip 校验失败、超限及未知二进制均返回错误。
const maxInspectBytes = 8 << 20

type generalWSReq struct {
	ReqIdentifier int
	Token         string
	SendID        string
	OperationID   string
	MsgIncr       string
	Data          []byte
}

func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 127 {
			return false
		}
	}
	return true
}

func decompressPayload(data []byte) ([]byte, bool, error) {
	if len(data) > maxInspectBytes {
		return nil, false, fmt.Errorf("帧超过 %d 字节", maxInspectBytes)
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, false, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, true, fmt.Errorf("gzip header: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxInspectBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("gzip 解压/校验失败: %w", err)
	}
	if len(out) > maxInspectBytes {
		return nil, true, fmt.Errorf("gzip 解压结果超限")
	}
	return out, true, nil
}

// parseOutgoing 只在审核副本上解码，绝不改写转发帧。
// reqIdentifier=1003 为 OpenIM 发送消息；其余协议请求直通。
// 支持原生 SDK 的 gzip+Gob、未压缩 Gob、JSON 外包及直接 JSON 消息。
func parseOutgoing(raw []byte) (msg openIMMsg, skip bool, format string, err error) {
	data, zipped, err := decompressPayload(raw)
	format = "raw"
	if zipped {
		format = "gzip"
	}
	if err != nil {
		return msg, false, format, err
	}
	// 先按二进制 Gob 解包，不扫描二进制中的花括号，也不提取可打印碎片。
	var req generalWSReq
	if len(data) > 0 && gob.NewDecoder(bytes.NewReader(data)).Decode(&req) == nil && req.ReqIdentifier != 0 {
		format += "+gob"
		if req.ReqIdentifier != 1003 {
			return msg, true, fmt.Sprintf("%s(req=%d)", format, req.ReqIdentifier), nil
		}
		msg, err = decodeBusiness(req.Data)
		if msg.SendID == "" {
			msg.SendID = req.SendID
		}
		return msg, false, format + "+body", err
	}
	if !utf8.Valid(data) {
		return msg, false, format, fmt.Errorf("未知二进制帧（不是有效 Gob/UTF-8）")
	}
	data = bytes.TrimSpace(data)
	for _, prefix := range []string{"s:", "m:"} {
		if bytes.HasPrefix(data, []byte(prefix)) {
			data = bytes.TrimSpace(data[len(prefix):])
			break
		}
	}
	if len(data) == 0 {
		return msg, false, format, fmt.Errorf("空消息")
	}
	if data[0] == '{' {
		format += "+json"
		var env struct {
			ReqIdentifier int             `json:"reqIdentifier"`
			SendID        string          `json:"sendID"`
			Data          json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal(data, &env); err != nil {
			return msg, false, format, err
		}
		if env.ReqIdentifier != 0 {
			if env.ReqIdentifier != 1003 {
				return msg, true, fmt.Sprintf("%s(req=%d)", format, env.ReqIdentifier), nil
			}
			body := bytes.TrimSpace(env.Data)
			if len(body) > 0 && body[0] == '"' {
				var s string
				if err = json.Unmarshal(body, &s); err != nil {
					return msg, false, format, err
				}
				body, err = base64.StdEncoding.DecodeString(s)
				if err != nil {
					body, err = base64.RawStdEncoding.DecodeString(s)
				}
				if err != nil {
					return msg, false, format, fmt.Errorf("data Base64: %w", err)
				}
			}
			msg, err = decodeBusiness(body)
			if msg.SendID == "" {
				msg.SendID = env.SendID
			}
			return msg, false, format, err
		}
		msg, err = decodeBusiness(data)
		return msg, false, format, err
	}
	if data[0] == '[' {
		return msg, false, format, fmt.Errorf("不支持批量 JSON 帧，不能漏审")
	}
	if !validText(string(data)) {
		return msg, false, format, fmt.Errorf("二进制/控制字符不能作为聊天正文")
	}
	// 保留旧网关对真正 UTF-8 纯文本的支持。
	msg.ContentType = 101
	msg.Content, _ = json.Marshal(string(data))
	return msg, false, format + "+text", nil
}

// decodeBusiness 按协议字段读取 MsgData，不能扫描第一个 JSON 猜正文。
// 当前 OpenIM MsgData: 1/2/3=发送者/接收者/群，11=contentType，12=content。
// 同时接受 SendMsgReq(msgData=3) 包装，及 JSON msg/msgData 包装。
func decodeBusiness(raw []byte) (msg openIMMsg, err error) {
	raw, _, err = decompressPayload(raw)
	if err != nil {
		return msg, err
	}
	if len(raw) == 0 {
		return msg, fmt.Errorf("发送消息 data 为空")
	}
	if utf8.Valid(raw) && json.Valid(raw) {
		data := bytes.TrimSpace(raw)
		var obj map[string]json.RawMessage
		if err = json.Unmarshal(data, &obj); err != nil {
			return msg, err
		}
		for _, key := range []string{"msg", "msgData"} {
			if nested, ok := obj[key]; ok {
				data = nested
				break
			}
		}
		if err = json.Unmarshal(data, &msg); err != nil {
			return msg, err
		}
		if len(msg.Content) == 0 {
			return msg, fmt.Errorf("JSON 消息缺少 content")
		}
		return msg, nil
	}
	fields, err := readWireFields(raw)
	if err != nil {
		return msg, fmt.Errorf("protobuf: %w", err)
	}
	// SendMsgReq 当前是 field 3 包装；仅在外层没有 MsgData 标记时尝试。
	if _, ok := fields[11]; !ok {
		if _, ok := fields[12]; !ok {
			if f, ok := fields[3]; ok && f.kind == 2 {
				nested, e := readWireFields(f.data)
				if e == nil {
					if _, exists := nested[11]; exists {
						fields = nested
					}
				}
			}
		}
	}
	ct, ok := fields[11]
	if !ok || ct.kind != 0 || ct.number == 0 || ct.number > 1<<31-1 {
		return msg, fmt.Errorf("缺少合法 MsgData.contentType(field 11)，请核对 SDK 协议版本")
	}
	msg.ContentType = int(ct.number)
	for n, dst := range map[int]*string{1: &msg.SendID, 2: &msg.RecvID, 3: &msg.GroupID} {
		if f, ok := fields[n]; ok {
			if f.kind != 2 || !validText(string(f.data)) {
				return msg, fmt.Errorf("非法 MsgData ID field=%d", n)
			}
			*dst = string(f.data)
		}
	}
	content, ok := fields[12]
	if !ok || content.kind != 2 {
		return msg, fmt.Errorf("缺少 MsgData.content(field 12)")
	}
	if !validText(string(content.data)) {
		return msg, fmt.Errorf("MsgData.content 非合法文本")
	}
	if json.Valid(content.data) {
		msg.Content = append(json.RawMessage(nil), content.data...)
	} else {
		msg.Content, _ = json.Marshal(string(content.data))
	}
	return msg, nil
}

type wireField struct {
	field  int
	kind   int
	number uint64
	data   []byte
}

func readWireFields(data []byte) (map[int]wireField, error) {
	fields, err := readWireList(data)
	if err != nil {
		return nil, err
	}
	out := make(map[int]wireField)
	for _, f := range fields {
		out[f.field] = f
	}
	return out, nil
}
func readWireList(data []byte) ([]wireField, error) {
	var out []wireField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 || tag>>3 > (1<<29)-1 {
			return nil, fmt.Errorf("非法 tag")
		}
		data = data[n:]
		field := int(tag >> 3)
		f := wireField{field: field, kind: int(tag & 7)}
		switch f.kind {
		case 0:
			f.number, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, fmt.Errorf("截断/溢出 varint")
			}
			data = data[n:]
		case 1, 5:
			size := 8
			if f.kind == 5 {
				size = 4
			}
			if len(data) < size {
				return nil, fmt.Errorf("截断 fixed 字段")
			}
			data = data[size:]
		case 2:
			length, used := binary.Uvarint(data)
			if used <= 0 {
				return nil, fmt.Errorf("非法长度")
			}
			data = data[used:]
			if length > uint64(len(data)) {
				return nil, fmt.Errorf("字段长度越界")
			}
			f.data = data[:int(length)]
			data = data[int(length):]
		default:
			return nil, fmt.Errorf("不支持 wire type=%d", f.kind)
		}
		// protobuf singular 字段遵循最后一次赋值；repeat/map 不参与正文提取。
		out = append(out, f)
	}
	return out, nil
}

func extractTextContent(raw json.RawMessage) string {
	var walk func(json.RawMessage, int) []string
	walk = func(b json.RawMessage, depth int) []string {
		if depth > 16 {
			return nil
		}
		b = bytes.TrimSpace(b)
		if len(b) == 0 || bytes.Equal(b, []byte("null")) {
			return nil
		}
		var s string
		if json.Unmarshal(b, &s) == nil {
			if json.Valid([]byte(s)) && (strings.HasPrefix(strings.TrimSpace(s), "{") || strings.HasPrefix(strings.TrimSpace(s), "[")) {
				return walk([]byte(s), depth+1)
			}
			return []string{s}
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(b, &obj) == nil && obj != nil {
			var all []string
			for _, key := range []string{"content", "text", "atText", "detailMsg", "defaultTips", "nestedContent", "textElem", "atTextElem", "quoteElem", "quoteMessage"} {
				if v, ok := obj[key]; ok {
					all = append(all, walk(v, depth+1)...)
				}
			}
			return all
		}
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) == nil {
			var all []string
			for _, v := range arr {
				all = append(all, walk(v, depth+1)...)
			}
			return all
		}
		return nil
	}
	return strings.Join(uniqueNonEmpty(walk(raw, 0)), "\n")
}

func extractImageURLs(raw []byte, imageMessage bool) []string {
	var found []string
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 16 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if s, ok := value.(string); ok && isImageKey(key) && (imageMessage || isImageURL(s)) {
					if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
						found = append(found, s)
					}
				}
				walk(value, depth+1)
			}
		case []any:
			for _, value := range x {
				walk(value, depth+1)
			}
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				walk(nested, depth+1)
			}
		}
	}
	var obj any
	if json.Unmarshal(raw, &obj) == nil {
		walk(obj, 0)
	}
	for _, u := range findHTTPURLs(raw) {
		if isImageURL(u) {
			found = append(found, u)
		}
	}
	return uniqueNonEmpty(found)
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	q := u.Query()
	for key := range q {
		switch strings.ToLower(key) {
		case "token", "accesskeyid", "signature":
			q.Set(key, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// isImageKey 判断 JSON key 是否与图片有关。
func isImageKey(k string) bool {
	switch strings.ToLower(k) {
	case "url", "imageurl", "image_url", "picurl", "pic_url",
		"pictureurl", "picture_url", "imgurl", "img_url", "sourceurl", "source_url":
		return true
	}
	return false
}

// isImageURL 依据扩展名/关键字判断是否为图片链接。
func isImageURL(u string) bool {
	lu := strings.ToLower(u)
	if strings.HasPrefix(lu, "http://") || strings.HasPrefix(lu, "https://") {
		if i := strings.IndexByte(lu, '?'); i >= 0 {
			lu = lu[:i]
		}
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".tiff", ".heic"} {
			if strings.HasSuffix(lu, ext) {
				return true
			}
		}
		return false
	}
	// 相对路径：只要像图片扩展名也收
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp"} {
		if strings.HasSuffix(lu, ext) {
			return true
		}
	}
	return false
}

// findHTTPURLs 扫描字节流，提取其中出现的所有 http(s) 链接。
// 使用 rune 遍历，避免把中文全角标点（如 "，"）当作 URL 终止符的边界判断出错。
func findHTTPURLs(data []byte) []string {
	// URL 中允许出现的字符（含 % 编码）
	const allowedPunct = "-._~:/?#[]@!$&'()*+,;=%"

	var urls []string
	runes := []rune(string(data))
	for i := 0; i < len(runes); i++ {
		if !hasHTTPPrefix(runes, i) {
			continue
		}
		j := i
		for j < len(runes) {
			r := runes[j]
			if r > 127 { // 非 ASCII（中文等）即认为 URL 结束
				break
			}
			if r == '"' || r == '\'' || r == '\\' || r == '<' || r == '>' || r == ' ' {
				break
			}
			if !(isAlphaNum(r) || strings.ContainsRune(allowedPunct, r)) {
				break
			}
			j++
		}
		u := strings.TrimRight(string(runes[i:j]), ".,;:!?)")
		if u != "" {
			urls = append(urls, u)
		}
		i = j - 1
	}
	return urls
}

func hasHTTPPrefix(runes []rune, i int) bool {
	for _, p := range []string{"http://", "https://"} {
		pr := []rune(p)
		if i+len(pr) <= len(runes) && string(runes[i:i+len(pr)]) == p {
			return true
		}
	}
	return false
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// ============================================================================
// 第四部分：WS 转发隧道（对外公共库入口）
//
// 与旧版最大的区别：不再使用包级全局 cfg / upgrader，
// 而是收敛到 Proxy 结构体，便于并发安全地在库中多次实例化。
// ============================================================================

// Proxy 是转发 + 审核代理（公共库主入口）。
// 通过 New 构造后，把 Handler() 挂到任意 http 路由即可。
type Proxy struct {
	cfg       Config
	moderator Moderator
	upgrader  websocket.Upgrader
	dialer    websocket.Dialer
}

// New 构造一个 Proxy。cfg 中的零值字段会用内置默认补齐。
// 配置无效时返回 error。
func New(cfg Config) (*Proxy, error) {
	cfg.applyDefaults()
	switch strings.ToLower(cfg.Moderation) {
	case "aliyun":
		if cfg.AppKeyID == "" || cfg.AppKeySecret == "" {
			return nil, fmt.Errorf("aliyun 模式必须配置 -ak/-sk 或 WEF_ALIYUN_AK/WEF_ALIYUN_SK")
		}
	case "local", "off", "none", "disable", "disabled":
	default:
		return nil, fmt.Errorf("未知 moderation=%q", cfg.Moderation)
	}

	p := &Proxy{
		cfg:       cfg,
		moderator: buildModerator(cfg),
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(*http.Request) bool { return true }, // 交由上游鉴权
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
		dialer: websocket.Dialer{
			HandshakeTimeout: cfg.DialTimeout,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
		},
	}
	return p, nil
}

// Moderator 返回当前使用的检验器（可能为 nil，表示 off）。
func (p *Proxy) Moderator() Moderator { return p.moderator }

// Config 返回生效的配置副本。
func (p *Proxy) Config() Config { return p.cfg }

// Handler 返回可供 http 注册的 WS 处理函数。
func (p *Proxy) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.serveWS(w, r)
	}
}

// wsHandler 是语义化便捷包装：一次性构造 Proxy 并返回其 Handler。
// 便于在 http.Handle("/ws", wsHandler(cfg)) 这类场景直接使用。
// 若需要访问 Proxy（如读取 Moderator），请改用 New() + (*Proxy).Handler()。
func wsHandler(cfg Config) http.HandlerFunc {
	p, err := New(cfg)
	if err != nil {
		// 将配置错误反馈给调用方。
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "wsHandler init failed: "+err.Error(), http.StatusInternalServerError)
		}
	}
	return p.Handler()
}

// serveWS 处理 App 的连接请求，并建立到上游 OpenIM 的双向隧道。
func (p *Proxy) serveWS(w http.ResponseWriter, r *http.Request) {
	// App 侧握手
	appConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("升级 App 连接失败: %v", err)
		return
	}
	defer appConn.Close()

	// 从 App 的 query 里取出全部参数（token / sendID / platformID 等），
	// OpenIM msggateway 会强制校验这些参数，必须原样透传给上游。
	appQuery := r.URL.Query()
	token := appQuery.Get("token")
	if token == "" {
		log.Println("警告: 连接未携带 token，若上游需要鉴权将失败")
	}
	if appQuery.Get("sendID") == "" || appQuery.Get("platformID") == "" {
		log.Printf("警告: 连接缺少 sendID/platformID，上游 msggateway 可能以 "+
			"1602 args err 拒绝握手 (sendID=%q platformID=%q)",
			appQuery.Get("sendID"), appQuery.Get("platformID"))
	}

	upstreamURL := buildUpstreamURL(p.cfg.Upstream, appQuery)
	log.Printf("新连接: remote=%s -> upstream=%s", r.RemoteAddr, redactURL(upstreamURL))

	// 连接上游 OpenIM
	upConn, _, err := p.dialer.Dial(upstreamURL, nil)
	if err != nil {
		log.Printf("连接上游失败: %v", err)
		_ = appConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "upstream dial failed"))
		return
	}
	defer upConn.Close()

	log.Printf("隧道建立成功: remote=%s", r.RemoteAddr)
	appConn.SetReadLimit(maxInspectBytes)
	p.proxy(appConn, upConn, appQuery.Get("sendID"))
	log.Printf("隧道关闭: remote=%s", r.RemoteAddr)
}

// buildUpstreamURL 组装上游 URL：以 base（-upstream 配置）为基准，
// 把 App 带来的原始 query 参数（token、sendID、platformID、operationID 等）
// 原样透传给 OpenIM msggateway。
//
// 为什么必须透传：OpenIM 的 msggateway.ParseEssentialArgs 强制校验
// token / sendID / platformID（platformID 还必须是整数），只带 token 会被
// 上游以 1602 args err 拒绝握手。
//
// 冲突处理：App 的 query 优先级高于 base 自带 query（同名覆盖），
// 避免 -upstream 上写死的旧参数压过 App 的真实参数。
func buildUpstreamURL(base string, appQuery url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := u.Query() // base 自带参数
	for k, vs := range appQuery {
		for _, v := range vs {
			query.Set(k, v)
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// proxy 建立两条方向的转发管道，任意一侧断开即整体结束。
func (p *Proxy) proxy(appConn, upConn *websocket.Conn, userID string) {
	// 控制帧不会由 ReadMessage 返回，必须显式桥接。
	bridgeControl(appConn, upConn, "App->OpenIM", userID, p.cfg.Debug)
	bridgeControl(upConn, appConn, "OpenIM->App", userID, p.cfg.Debug)
	var appWriteMu, upWriteMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)

	done := make(chan struct{})
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			close(done)
			_ = appConn.Close()
			_ = upConn.Close()
		})
	}

	// App -> OpenIM（审核先行：同步模式下命中违规不转发）
	//
	// check 返回 blocked=true 时，pump 会丢弃该帧，不写入上游 OpenIM。
	check := func(messageType int, data []byte) bool {
		if p.cfg.Debug {
			logFrame("App->OpenIM", userID, data)
		}
		if messageType == websocket.TextMessage && (string(data) == "ping" || string(data) == "pong") {
			return false
		}
		blocked := inspectOutgoing(p.moderator, p.cfg.CheckTimeout, p.cfg.ModerationAsync, p.cfg.Debug, userID, data)
		if blocked {
			reply, err := buildRejectResponse(data)
			if err != nil {
				log.Printf("[失败通知] trace=%s user=%s result=无法构造响应 err=%v", frameTrace(data), userID, err)
			} else {
				err = writeDataFrame(appConn, &appWriteMu, messageType, reply)
				log.Printf("[失败通知] trace=%s user=%s errCode=%d message=%q writeErr=%v", frameTrace(data), userID, gatewayRejectCode, gatewayRejectMessage, err)
				if err != nil {
					closeAll()
				}
			}
		}
		return blocked
	}
	go func() {
		defer wg.Done()
		defer closeAll()
		pump(appConn, upConn, check, "App->OpenIM", userID, &upWriteMu)
	}()

	// OpenIM -> App（纯转发，不审核）
	go func() {
		defer wg.Done()
		defer closeAll()
		pump(upConn, appConn, func(_ int, data []byte) bool {
			if p.cfg.Debug {
				logFrame("OpenIM->App", userID, data)
			}
			return false
		}, "OpenIM->App", userID, &appWriteMu)
	}()

	wg.Wait()
}

// pump 从 src 读消息并写到 dst。
//
// hook 语义（可为 nil）：
//   - 返回 false：放行，继续转发该帧；
//   - 返回 true ：拦截原帧，向上游发送 Ping 保活，不发送业务成功回执。
//
// 在同步审核模式下 hook 会阻塞（等待审核接口返回或超时），
// 此时 App->上游 这条管道被串行化；反方向管道不受影响。
func pump(src, dst *websocket.Conn, hook func(messageType int, data []byte) bool, direction, userID string, writeMu *sync.Mutex) {
	for {
		messageType, data, err := src.ReadMessage()
		if err != nil {
			log.Printf("[隧道读取结束] direction=%s user=%s remote=%s err=%v", direction, userID, src.RemoteAddr(), err)
			return
		}
		// 拦截时仅发送标准 WS Ping 控制帧；不携带正文，也不伪造发送成功。
		// 解析失败/审核失败的拦截同样使用保活，原有同步/异步判定保持不变。
		if hook != nil && hook(messageType, data) {
			trace := frameTrace(data)
			log.Printf("[转发结果] trace=%s direction=%s user=%s result=原消息已拦截未转发", trace, direction, userID)
			if direction == "App->OpenIM" {
				if err := dst.WriteControl(websocket.PingMessage, []byte("wef-blocked-keepalive"), time.Now().Add(5*time.Second)); err != nil {
					log.Printf("[拦截保活] trace=%s user=%s type=ping result=发送失败 err=%v", trace, userID, err)
					return
				}
				log.Printf("[拦截保活] trace=%s user=%s type=ping result=已发送 等待上游Pong，连接继续保留", trace, userID)
			}
			continue
		}
		if err := writeDataFrame(dst, writeMu, messageType, data); err != nil {
			log.Printf("[转发结果] trace=%s direction=%s user=%s result=写入失败 err=%v", frameTrace(data), direction, userID, err)
			return
		}
		log.Printf("[转发结果] trace=%s direction=%s user=%s result=写入对端成功 bytes=%d", frameTrace(data), direction, userID, len(data))
	}
}

// ============================================================================
// 第五部分：HTTP 服务与命令行入口
//
// 库使用者只需 New() + Handler()；
// 命令行使用者直接运行编译后的 exe（配置优先级：参数 > 环境变量 > 常量）。
// ============================================================================

// Server 包住 http.Server，提供优雅停机。
type Server struct {
	proxy *Proxy
	http  *http.Server
}

// NewServer 构造一个可运行的 HTTP 服务。
func NewServer(cfg Config) (*Server, error) {
	p, err := New(cfg)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", p.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return &Server{
		proxy: p,
		http: &http.Server{
			Addr:              p.cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

// Serve 阻塞式启动服务（可用 ctx 取消，实现优雅停机）。
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.http.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// parseFlags 解析命令行参数：以 newDefaultConfig() 为底，
// 仅当用户显式传入时才覆盖（实现 参数 > 环境变量 > 常量）。
func parseFlags(args []string) (Config, error) {
	c := newDefaultConfig()
	fs := flag.NewFlagSet("wef", flag.ContinueOnError)

	fs.StringVar(&c.Addr, "addr", c.Addr, "本地监听地址")
	fs.StringVar(&c.Upstream, "upstream", c.Upstream, "OpenIM msggateway 的 ws 地址")
	fs.StringVar(&c.Moderation, "moderation", c.Moderation, "违规检验模式: local | aliyun | off")
	fs.BoolVar(&c.ModerationAsync, "moderation-async", c.ModerationAsync, "true=异步旁路(不阻断) false=同步拦截(默认)")
	fs.BoolVar(&c.Debug, "debug", c.Debug, "true=打印详细请求/响应日志(排查阿里云是否被调用)")
	fs.StringVar(&c.AppKeyID, "ak", c.AppKeyID, "阿里云 AccessKeyId")
	fs.StringVar(&c.AppKeySecret, "sk", c.AppKeySecret, "阿里云 AccessKeySecret")
	fs.StringVar(&c.Region, "region", c.Region, "阿里云 region，如 cn-shanghai")
	fs.StringVar(&c.Endpoint, "endpoint", c.Endpoint, "阿里云绿网网关，留空自动推导")
	fs.StringVar(&c.TextService, "text-service", c.TextService, "文本检测 service，默认 chat_detection")
	fs.StringVar(&c.ImageService, "image-service", c.ImageService, "图片检测 service，默认 baselineCheck")
	fs.StringVar(&c.ViolationCallback, "callback", c.ViolationCallback, "命中违规后的回调 URL（可选）")
	fs.DurationVar(&c.DialTimeout, "dial-timeout", c.DialTimeout, "连接上游超时")
	fs.DurationVar(&c.CheckTimeout, "check-timeout", c.CheckTimeout, "单次检验超时")

	if err := fs.Parse(args); err != nil {
		return c, err
	}
	c.applyDefaults()
	return c, nil
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("解析参数失败: %v", err)
	}

	srv, err := NewServer(cfg)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	if m := srv.proxy.Moderator(); m != nil {
		log.Printf("违规词检验已启用: mode=%s", m.Name())
	} else {
		log.Printf("违规词检验已关闭 (off)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("转发 WS 服务已启动: %s -> %s", cfg.Addr, cfg.Upstream)
	if err := srv.Serve(ctx); err != nil {
		log.Fatalf("服务异常退出: %v", err)
	}
	log.Println("服务已优雅退出")
}

// WriteControl 可与数据帧写入并发执行，不引入多个 WriteMessage 写协程。
func bridgeControl(src, dst *websocket.Conn, direction, userID string, debug bool) {
	forward := func(typ int, name, payload string) error {
		err := dst.WriteControl(typ, []byte(payload), time.Now().Add(5*time.Second))
		if debug || err != nil {
			log.Printf("[心跳透传] direction=%s user=%s type=%s err=%v", direction, userID, name, err)
		}
		return err
	}
	src.SetPingHandler(func(s string) error { return forward(websocket.PingMessage, "ping", s) })
	src.SetPongHandler(func(s string) error { return forward(websocket.PongMessage, "pong", s) })
}

// 记录外包元信息及接收推送正文；不将接收推送送审，不记录 token。
func logFrame(direction, userID string, raw []byte) {
	data, zipped, err := decompressPayload(raw)
	if err != nil {
		log.Printf("[帧观察] direction=%s user=%s bytes=%d decodeErr=%v", direction, userID, len(raw), err)
		return
	}
	var header struct {
		ReqIdentifier int
		ErrCode       int
		MsgIncr       string
		OperationID   string
		Data          []byte
	}
	format := "json"
	if gob.NewDecoder(bytes.NewReader(data)).Decode(&header) == nil && header.ReqIdentifier != 0 {
		format = "gob"
	} else {
		header.ReqIdentifier = 0
		if json.Unmarshal(data, &header) != nil {
			format = "other"
		}
	}
	if zipped {
		format = "gzip+" + format
	}
	log.Printf("[帧观察] direction=%s user=%s format=%s req=%d errCode=%d bytes=%d msgIncr=%s operationID=%s",
		direction, userID, format, header.ReqIdentifier, header.ErrCode, len(raw), header.MsgIncr, header.OperationID)
	if direction == "OpenIM->App" && header.ReqIdentifier == 2001 {
		msgs, err := decodePushMessages(header.Data)
		if err != nil {
			log.Printf("[推送解析失败] trace=%s user=%s err=%v 本帧仍原样转发", frameTrace(raw), userID, err)
			return
		}
		for _, msg := range msgs {
			logMessageBody("接收推送", frameTrace(raw), userID, msg)
		}
		log.Printf("[未送审] trace=%s aliyun=否 reason=服务端接收推送，本帧未调用审核；无法据此判断原发送入口是否审核 messages=%d", frameTrace(raw), len(msgs))
	}
}

// trace 是原始帧的短摘要，用来串起正文、审核和转发日志；不是业务消息 ID。
func frameTrace(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8])
}
func logMessageBody(kind, trace, user string, msg openIMMsg) {
	log.Printf("[%s] trace=%s connectionUser=%s sendID=%s recvID=%s groupID=%s contentType=%d text=%q content=%q contentBytes=%d truncated=%t",
		kind, trace, user, msg.SendID, msg.RecvID, msg.GroupID, msg.ContentType,
		truncate(extractTextContent(msg.Content), 4096), truncate(string(msg.Content), 4096),
		len(msg.Content), utf8.RuneCount(msg.Content) > 4096)
}

// OpenIM PushMessages: map<string,PullMsgs> msgs=1/notificationMsgs=2;
// map entry: key=1,value=2; PullMsgs: repeated MsgData Msgs=1。
// 按字段层级解析并保留所有 repeated 值，避免一个推送只打印最后一条消息。
func decodePushMessages(raw []byte) ([]openIMMsg, error) {
	data, _, err := decompressPayload(raw)
	if err != nil {
		return nil, err
	}
	// 兼容直接 MsgData 推送。
	if msg, e := decodeBusiness(data); e == nil {
		return []openIMMsg{msg}, nil
	}
	fields, err := readWireList(data)
	if err != nil {
		return nil, err
	}
	var out []openIMMsg
	for _, bucket := range fields {
		if (bucket.field != 1 && bucket.field != 2) || bucket.kind != 2 {
			continue
		}
		entry, e := readWireFields(bucket.data)
		if e != nil {
			return nil, e
		}
		value, ok := entry[2]
		if !ok || value.kind != 2 {
			return nil, fmt.Errorf("未知推送 map entry 格式")
		}
		list, e := readWireList(value.data)
		if e != nil {
			return nil, e
		}
		for _, item := range list {
			if item.field != 1 || item.kind != 2 {
				continue
			}
			msg, e := decodeBusiness(item.data)
			if e != nil {
				return nil, fmt.Errorf("推送 MsgData: %w", e)
			}
			out = append(out, msg)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未找到支持的 PushMessages/MsgData 正文，bytes=%d", len(data))
	}
	return out, nil
}

// 网关自定义拒绝码。客户端 SDK 应将非零 ErrCode 作为此次请求失败处理。
const gatewayRejectCode = 40301
const gatewayRejectMessage = "消息未通过内容审核，发送失败"

type generalWSResp struct {
	ReqIdentifier int    `json:"reqIdentifier"`
	ErrCode       int    `json:"errCode"`
	ErrMsg        string `json:"errMsg"`
	MsgIncr       string `json:"msgIncr"`
	OperationID   string `json:"operationID"`
	Data          []byte `json:"data"`
}

// 与服务端推送共用写锁，避免审核回执和推送并发 WriteMessage 导致连接损坏。
func writeDataFrame(conn *websocket.Conn, mu *sync.Mutex, typ int, data []byte) error {
	mu.Lock()
	defer mu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	defer conn.SetWriteDeadline(time.Time{})
	return conn.WriteMessage(typ, data)
}

// 回执保留请求关联字段，并使用原请求的 Gob/JSON、gzip 格式。
// 无法识别的帧不伪造协议回执，仍拦截原帧并记录失败原因。
func buildRejectResponse(raw []byte) ([]byte, error) {
	data, zipped, err := decompressPayload(raw)
	if err != nil {
		return nil, err
	}
	var req generalWSReq
	isGob := gob.NewDecoder(bytes.NewReader(data)).Decode(&req) == nil && req.ReqIdentifier != 0
	if !isGob {
		// 只读取回执需要的元信息，业务 Data 可能是对象而不是 Base64。
		var meta struct {
			ReqIdentifier int    `json:"reqIdentifier"`
			MsgIncr       string `json:"msgIncr"`
			OperationID   string `json:"operationID"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("无法识别请求外包: %w", err)
		}
		req.ReqIdentifier, req.MsgIncr, req.OperationID = meta.ReqIdentifier, meta.MsgIncr, meta.OperationID
	}
	if req.ReqIdentifier != 1003 {
		return nil, fmt.Errorf("无可关联的发送请求 req=%d", req.ReqIdentifier)
	}
	resp := generalWSResp{
		ReqIdentifier: req.ReqIdentifier, ErrCode: gatewayRejectCode, ErrMsg: gatewayRejectMessage,
		MsgIncr: req.MsgIncr, OperationID: req.OperationID,
	}
	var encoded []byte
	if isGob {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(resp); err != nil {
			return nil, err
		}
		encoded = buf.Bytes()
	} else {
		encoded, err = json.Marshal(resp)
		if err != nil {
			return nil, err
		}
	}
	if !zipped {
		return encoded, nil
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(encoded); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
