//go:build unit

package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractGeminiCLISessionHash(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		privilegedUserID string
		wantEmpty        bool
		wantHash         string
	}{
		{
			name:             "with privileged-user-id and tmp dir",
			body:             `{"contents":[{"parts":[{"text":"The project's temporary directory is: /Users/test-user/.gemini/tmp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`,
			privilegedUserID: "00000000-0000-4000-8000-000000000001",
			wantEmpty:        false,
			wantHash: func() string {
				combined := "00000000-0000-4000-8000-000000000001:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				hash := sha256.Sum256([]byte(combined))
				return hex.EncodeToString(hash[:])
			}(),
		},
		{
			name:             "without privileged-user-id but with tmp dir",
			body:             `{"contents":[{"parts":[{"text":"The project's temporary directory is: /Users/test-user/.gemini/tmp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`,
			privilegedUserID: "",
			wantEmpty:        false,
			wantHash:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:             "without tmp dir",
			body:             `{"contents":[{"parts":[{"text":"Hello world"}]}]}`,
			privilegedUserID: "00000000-0000-4000-8000-000000000001",
			wantEmpty:        true,
		},
		{
			name:             "empty body",
			body:             "",
			privilegedUserID: "00000000-0000-4000-8000-000000000001",
			wantEmpty:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建测试上下文
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/test", nil)
			if tt.privilegedUserID != "" {
				c.Request.Header.Set("x-gemini-api-privileged-user-id", tt.privilegedUserID)
			}

			// 调用函数
			result := extractGeminiCLISessionHash(c, []byte(tt.body))

			// 验证结果
			if tt.wantEmpty {
				require.Empty(t, result, "expected empty session hash")
			} else {
				require.NotEmpty(t, result, "expected non-empty session hash")
				require.Equal(t, tt.wantHash, result, "session hash mismatch")
			}
		})
	}
}

func TestGeminiCLITmpDirRegex(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMatch bool
		wantHash  string
	}{
		{
			name:      "valid tmp dir path",
			input:     "/Users/test-user/.gemini/tmp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantMatch: true,
			wantHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:      "valid tmp dir path in text",
			input:     "The project's temporary directory is: /Users/test-user/.gemini/tmp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nOther text",
			wantMatch: true,
			wantHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:      "invalid hash length",
			input:     "/Users/test-user/.gemini/tmp/abc123",
			wantMatch: false,
		},
		{
			name:      "no tmp dir",
			input:     "Hello world",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := geminiCLITmpDirRegex.FindStringSubmatch(tt.input)
			if tt.wantMatch {
				require.NotNil(t, match, "expected regex to match")
				require.Len(t, match, 2, "expected 2 capture groups")
				require.Equal(t, tt.wantHash, match[1], "hash mismatch")
			} else {
				require.Nil(t, match, "expected regex not to match")
			}
		})
	}
}

func TestSafeShortPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{name: "空字符串", input: "", n: 8, want: ""},
		{name: "长度小于截断值", input: "abc", n: 8, want: "abc"},
		{name: "长度等于截断值", input: "12345678", n: 8, want: "12345678"},
		{name: "长度大于截断值", input: "1234567890", n: 8, want: "12345678"},
		{name: "截断值为0", input: "123456", n: 0, want: "123456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeShortPrefix(tt.input, tt.n))
		})
	}
}
