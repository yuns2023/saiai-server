package service

import (
	"fmt"
	"html"
	"mime"
	"net/url"
	"strings"
)

// Transactional email look shared by every site: a white "receipt" on a cool
// grey shell, the site name as a text wordmark over a double rule, ink-blue
// text, one blue action. Table layout + inline styles only, no images and no
// <style> block, so QQ Mail, Gmail and Outlook render it the same way.
const (
	emailInk     = "#14203a"
	emailInk2    = "#47536e"
	emailInk3    = "#66708a"
	emailRule    = "#d9dee7"
	emailShell   = "#f1f3f7"
	emailBlue    = "#1d4e89"
	emailFont    = "-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',Roboto,Helvetica,Arial,sans-serif"
	emailMonoFnt = "'SFMono-Regular',Menlo,Consolas,'Liberation Mono','Courier New',monospace"
)

// emailLayout wraps already-escaped body HTML in the shared frame. preheader is
// the inbox preview line (plain text, escaped here).
func emailLayout(siteName, preheader, bodyHTML string) string {
	name := html.EscapeString(strings.TrimSpace(siteName))
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>` + name + `</title>
</head>
<body style="margin:0;padding:0;background-color:` + emailShell + `;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;color:transparent;">` + html.EscapeString(preheader) + `</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:` + emailShell + `;">
<tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:480px;background-color:#ffffff;border:1px solid ` + emailRule + `;">
<tr><td style="padding:28px 32px 0 32px;font-family:` + emailFont + `;">
<div style="font-size:18px;line-height:24px;font-weight:600;color:` + emailInk + `;letter-spacing:-0.01em;">` + name + `</div>
<div style="margin-top:14px;border-bottom:3px double ` + emailInk + `;font-size:0;line-height:0;">&nbsp;</div>
</td></tr>
<tr><td style="padding:24px 32px 8px 32px;font-family:` + emailFont + `;font-size:14px;line-height:22px;color:` + emailInk2 + `;">
` + bodyHTML + `
</td></tr>
<tr><td style="padding:16px 32px 28px 32px;font-family:` + emailFont + `;">
<div style="border-top:1px solid ` + emailRule + `;padding-top:14px;font-size:12px;line-height:18px;color:` + emailInk3 + `;">此邮件由 ` + name + ` 自动发送，请勿直接回复。</div>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>
`
}

func emailHeading(text string) string {
	return `<div style="font-size:16px;line-height:24px;font-weight:600;color:` + emailInk + `;margin:0 0 8px 0;">` + html.EscapeString(text) + `</div>`
}

func emailParagraph(escapedHTML string) string {
	return `<p style="margin:0 0 12px 0;">` + escapedHTML + `</p>`
}

// buildVerifyCodeEmailBody renders the one-time code mail. The same code is
// used for sign-up, binding TOTP and other email checks, so the wording stays
// neutral about why it was requested.
func buildVerifyCodeEmailBody(code, siteName string) string {
	digits := html.EscapeString(code)
	body := emailHeading("邮箱验证码") +
		emailParagraph("你正在验证此邮箱，请在页面中输入下面的验证码：") +
		`<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:8px 0 16px 0;">
<tr><td style="border:1px solid ` + emailRule + `;border-left:3px solid ` + emailBlue + `;padding:14px 22px;font-family:` + emailMonoFnt + `;font-size:30px;line-height:36px;font-weight:600;letter-spacing:8px;color:` + emailInk + `;">` + digits + `</td></tr>
</table>` +
		emailParagraph(fmt.Sprintf("验证码 <strong style=\"color:%s;\">%d 分钟</strong>内有效，只能使用一次。", emailInk, int(verifyCodeTTL.Minutes()))) +
		emailParagraph("如果这不是你本人的操作，请忽略此邮件。")
	return emailLayout(siteName, "你的验证码是 "+code, body)
}

// buildPasswordResetEmailBody renders the reset-link mail. resetURL must be an
// http(s) URL; anything else is shown as text only and never made clickable.
func buildPasswordResetEmailBody(resetURL, siteName string) string {
	escURL := html.EscapeString(resetURL)
	button := ""
	if isHTTPURL(resetURL) {
		button = `<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:8px 0 16px 0;">
<tr><td style="background-color:` + emailBlue + `;">
<a href="` + escURL + `" target="_blank" rel="noopener" style="display:inline-block;padding:11px 26px;font-family:` + emailFont + `;font-size:14px;line-height:20px;font-weight:600;color:#ffffff;text-decoration:none;">设置新密码</a>
</td></tr>
</table>`
	}
	body := emailHeading("重置密码") +
		emailParagraph("我们收到了重置此账号密码的请求。点击下方按钮设置新密码：") +
		button +
		emailParagraph(fmt.Sprintf("链接 <strong style=\"color:%s;\">%d 分钟</strong>内有效。", emailInk, int(passwordResetTokenTTL.Minutes()))) +
		emailParagraph("如果这不是你本人的操作，请忽略此邮件，你的密码不会改变。") +
		`<div style="margin:4px 0 8px 0;padding:10px 12px;background-color:` + emailShell + `;font-size:12px;line-height:18px;color:` + emailInk3 + `;word-break:break-all;">按钮无法打开时，复制以下链接到浏览器：<br>` + escURL + `</div>`
	return emailLayout(siteName, "重置你的密码", body)
}

// BuildTestEmail returns the subject and HTML for the admin "send test email" button.
func BuildTestEmail(siteName string) (subject, body string) {
	content := emailHeading("邮件配置成功") +
		emailParagraph("这是一封测试邮件。能收到它，说明当前 SMTP 设置可以正常发信。")
	return EmailSubject(siteName, "测试邮件"), emailLayout(siteName, "SMTP 设置可以正常发信", content)
}

// EmailSubject formats "[site] topic".
func EmailSubject(siteName, topic string) string {
	return fmt.Sprintf("[%s] %s", strings.TrimSpace(siteName), topic)
}

// encodeHeaderValue strips CR/LF (no header injection via site name) and
// RFC 2047-encodes non-ASCII text so every client shows the subject correctly.
func encodeHeaderValue(v string) string {
	v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
	return mime.BEncoding.Encode("UTF-8", v)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
