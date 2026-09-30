package main

import (
	"context"
	"errors"
	"net"
	"strings"
)

type networkFailure struct {
	Host  string
	Cause error
}

func (e *networkFailure) Error() string { return e.Host + "：" + safeNetworkError(e.Cause) }
func (e *networkFailure) Unwrap() error { return e.Cause }

func failureCode(err error) string {
	var dns *DNSFailure
	if errors.As(err, &dns) {
		return dns.Code
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "connection_timeout"
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "certificate") || strings.Contains(s, "tls:") {
		return "tls_error"
	}
	if strings.Contains(s, "blocked") || strings.Contains(s, "pinned") || strings.Contains(s, "blacklist") || strings.Contains(s, "拉黑") || strings.Contains(s, "固定") {
		return "node_policy"
	}
	if strings.Contains(s, "下载域名尚未接入") {
		return "redirect_unregistered"
	}
	return "connection_error"
}
func diagnosis(code string) (status, message, advice string) {
	switch code {
	case "dns_no_aaaa":
		return "未查到 IPv6", "DNS 已响应，但本次未返回公网 AAAA 记录", "可重测或寻找同一文件的官方 IPv6 / 双栈入口；不能靠切换任意 IP 让服务器支持 IPv6。"
	case "dns_nxdomain":
		return "域名不存在", "DNS 返回 NXDOMAIN（域名不存在）", "核对官网当前下载域名；旧地址可能已停用，重新检测确认。"
	case "dns_timeout":
		return "DNS 超时", "IPv6 DNS 查询超时，尚不能判断网站是否支持 IPv6", "先重测；若多个来源相同，检查本机 IPv6 到 DNS 服务的连通性。"
	case "dns_cname_loop":
		return "DNS 异常", "DNS 别名链循环或过长", "重新检测或核对官方域名，暂不接管此地址。"
	case "dns_error":
		return "DNS 异常", "DNS 查询未完成，尚不能判断网站是否支持 IPv6", "查看下方各 DNS 服务结果后重测，不要据此认定网站没有 IPv6。"
	case "connection_timeout":
		return "连接超时", "IPv6 连接或读取超时，无 IPv4 回退", "刷新上游 DNS，恢复自动选择，并检查黑名单；仍失败则检查当前网络路由。"
	case "tls_error":
		return "证书 / TLS 失败", "TLS 握手或证书校验失败", "刷新 DNS 并尝试其他当前 IPv6 节点；保持证书校验，不使用来历不明的固定 IP。"
	case "node_policy":
		return "节点策略阻止", "固定 IP 或黑名单限制了连接", "在上游管理中恢复自动，并按需解除拉黑；恢复自动不会清空黑名单。"
	case "redirect_unregistered":
		return "下载域名待接入", "下载重定向到了尚未登记的域名", "核对该域名属于官方文件服务后再接入；不会自动放行未知域名。"
	case "access_required":
		return "需授权 / 网站验证", "服务器拒绝了当前文件请求", "在原网站完成已有账户登录或访问验证，使用有效文件地址；本助手不导入登录状态。"
	case "sample_missing":
		return "需文件验证", "已解析到 IPv6，但尚未验证实际下载文件", "在原站取得有权限的 HTTPS 文件链接，在“科研下载”中验证；DNS 成功不代表文件成功。"
	case "sample_expired":
		return "样本地址失效", "服务器返回 404 或 410", "从官网重新取得该文件的当前下载链接，再验证文件内容。"
	case "rate_limited":
		return "请求受限", "服务器限制请求频率", "等待后重测，遵循服务器重试时间，避免连续请求。"
	case "http_error":
		return "服务器响应异常", "未收到成功的文件响应", "稍后重试并核对官方文件链接。"
	case "sample_format":
		return "文件格式不符", "返回内容不是预期文件，可能是登录页或错误页", "在原站核对文件链接和授权；不能把网页保存成功算作下载通过。"
	case "sample_hash":
		return "文件校验失败", "下载内容与预期 SHA256 不一致", "不要使用该文件；重新取得官方文件与校验值后重试。"
	case "sample_read":
		return "样本读取未完成", "读取中断、超过检测大小上限或服务器忽略范围请求", "可重测，或使用实际下载任务验证完整文件。"
	case "canceled":
		return "检测已取消", "检测已取消", "需要时重新检测。"
	default:
		return "连接失败", "IPv6 连接未完成，无 IPv4 回退", "刷新 DNS 并恢复自动节点选择，再核对实际文件地址；检查当前网络的 IPv6 路由。"
	}
}

func setDiagnosis(h *SourceHealth, code, detail string) {
	h.Code = code
	h.Status, h.Detail, h.Advice = diagnosis(code)
	if detail != "" {
		h.Detail = detail
	}
}
