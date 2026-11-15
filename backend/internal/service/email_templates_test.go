//go:build unit

package service

import (
	"context"
	"encoding/json"
	"mime"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildVerifyCodeEmailBody_ChineseAndEscaped(t *testing.T) {
	body := buildVerifyCodeEmailBody("097680", `CCYou<script>alert(1)</script>&"`)
	require.Contains(t, body, "097680")
	require.Contains(t, body, "邮箱验证码")
	require.Contains(t, body, "15 分钟")
	require.NotContains(t, body, "<script>")
	require.Contains(t, body, "CCYou&lt;script&gt;")
	require.NotContains(t, body, "Your verification code", "no English leftovers")
	require.NotContains(t, body, "linear-gradient", "old purple header is gone")
	require.NotContains(t, body, "<style", "inline styles only")
	require.NotContains(t, body, "<img", "no images: mail clients block data URIs")
}

func TestBuildPasswordResetEmailBody_LinkHandling(t *testing.T) {
	good := "https://ccyou.dev/reset-password?email=a%40b.c&token=abc"
	body := buildPasswordResetEmailBody(good, "CCYou.dev")
	require.Contains(t, body, `href="https://ccyou.dev/reset-password?email=a%40b.c&amp;token=abc"`)
	require.Contains(t, body, "30 分钟")
	require.Contains(t, body, "设置新密码")

	// Non-http(s) URLs never become a clickable button.
	bad := buildPasswordResetEmailBody(`javascript:alert(1)"><b>x`, "S")
	require.NotContains(t, bad, `href="javascript`)
	require.NotContains(t, bad, "<b>x")
	require.Contains(t, bad, "javascript:alert(1)&#34;&gt;&lt;b&gt;x")
}

func TestBuildTestEmail(t *testing.T) {
	subject, body := BuildTestEmail(" CCYou.dev ")
	require.Equal(t, "[CCYou.dev] 测试邮件", subject)
	require.Contains(t, body, "邮件配置成功")
}

func TestEncodeHeaderValue(t *testing.T) {
	enc := encodeHeaderValue("[CCYou.dev] 邮箱验证码\r\nBcc: x@y.z")
	require.NotContains(t, enc, "\r")
	require.NotContains(t, enc, "\n")
	dec, err := new(mime.WordDecoder).DecodeHeader(enc)
	require.NoError(t, err)
	require.Equal(t, "[CCYou.dev] 邮箱验证码  Bcc: x@y.z", dec)
	require.Equal(t, "plain ascii", encodeHeaderValue("plain ascii"))
}

func TestPublicSettings_InvitationCodeHint(t *testing.T) {
	repo := &settingPublicRepoStub{values: map[string]string{
		SettingKeyInvitationCodeEnabled: "true",
		SettingKeyInvitationCodeHint:    "  加群获取：https://t.me/example  ",
	}}
	svc := NewSettingService(repo, &config.Config{})

	pub, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "加群获取：https://t.me/example", pub.InvitationCodeHint)

	// The injected window.__APP_CONFIG__ is what the AOG UI reads first.
	injected, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(injected)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(raw), `"invitation_code_hint":"加群获取：https://t.me/example"`), string(raw))
}
