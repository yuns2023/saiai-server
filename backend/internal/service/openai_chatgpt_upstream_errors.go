package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const chatGPTErrorPrefixLimit = 16 * 1024

// Observe errors without buffering a successful stream, changing the provider
// response, retrying, or treating a browser challenge as invalid OAuth.
func observeChatGPTUpstreamResult(c *gin.Context, account *Account, resp *http.Response, err error, started time.Time) (*http.Response, error) {
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		if !isOpenAIProviderAttemptBudgetError(err) {
			message := "Upstream ChatGPT request failed"
			setOpsUpstreamError(c, 0, message, "")
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform: PlatformOpenAI, AccountID: account.ID,
				Passthrough: true, Kind: "request_error", Message: message,
			})
		}
		return resp, err
	}
	if resp == nil || resp.StatusCode < http.StatusBadRequest {
		return resp, nil
	}

	status := resp.StatusCode
	message := fmt.Sprintf("Upstream ChatGPT returned HTTP %d", status)
	setOpsUpstreamError(c, status, message, "")
	report := func(prefix []byte) {
		// Retain fixed diagnostic labels only. HTML can contain challenge tokens,
		// and JSON can contain private provider data; neither goes in the event.
		lower := strings.ToLower(string(prefix))
		html := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") ||
			strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html")
		challenge := strings.EqualFold(strings.TrimSpace(resp.Header.Get("cf-mitigated")), "challenge") ||
			(html && strings.Contains(lower, "challenge-platform"))
		if challenge {
			message = fmt.Sprintf("Upstream ChatGPT returned a browser verification page (HTTP %d)", status)
		} else if html {
			message = fmt.Sprintf("Upstream ChatGPT returned an HTML error page (HTTP %d)", status)
		}
		setOpsUpstreamError(c, status, message, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: PlatformOpenAI, AccountID: account.ID, Passthrough: true,
			UpstreamStatusCode: status, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "http_error", Message: message,
		})
	}
	if resp.Body == nil {
		report(nil)
	} else {
		resp.Body = &chatGPTUpstreamErrorBody{ReadCloser: resp.Body, report: report}
	}
	return resp, nil
}

type chatGPTUpstreamErrorBody struct {
	io.ReadCloser
	prefix []byte
	report func([]byte)
}

func (b *chatGPTUpstreamErrorBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.report != nil {
		if remaining := chatGPTErrorPrefixLimit - len(b.prefix); remaining > 0 {
			b.prefix = append(b.prefix, p[:min(n, remaining)]...)
		}
		if err != nil {
			b.finish()
		}
	}
	return n, err
}

func (b *chatGPTUpstreamErrorBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *chatGPTUpstreamErrorBody) finish() {
	if b.report != nil {
		b.report(b.prefix)
		b.report = nil
		b.prefix = nil
	}
}
