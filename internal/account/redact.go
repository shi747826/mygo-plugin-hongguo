package account

import (
	"regexp"
	"strings"
)

var (
	// 账号相关的敏感片段：Cookie、token 与任何绝对地址。
	tokenPattern = regexp.MustCompile(`(?i)(sessionid|sessionid_ss|sid_tt|sid_guard|passport_csrf_token|token)=[^;&\s]+`)
	urlPattern   = regexp.MustCompile(`(?i)https?://\S+`)
)

// redact 去掉错误文本里可能出现的地址与会话凭据，保证日志安全。
func redact(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	text = urlPattern.ReplaceAllString(text, "[地址]")
	text = tokenPattern.ReplaceAllString(text, "$1=[已隐藏]")
	if len(text) > 160 {
		text = text[:160] + "…"
	}
	return text
}
