//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexModelsHTTPUpstreamStub struct {
	response *http.Response
	err      error
	request  *http.Request
}

func (s *codexModelsHTTPUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.request = req
	return s.response, s.err
}

func (s *codexModelsHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ bool) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestFetchCodexModelsManifestUpstream_ConvertsStandardModelList(t *testing.T) {
	responseHeaders := make(http.Header)
	responseHeaders.Set("ETag", `"upstream"`)
	upstream := &codexModelsHTTPUpstreamStub{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     responseHeaders,
		Body: io.NopCloser(strings.NewReader(`{
			"object":"list",
			"data":[
				{"id":"gpt-5.6-sol","object":"model"},
				{"id":"vendor-new-preview","object":"model"}
			]
		}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	manifest, err := svc.fetchCodexModelsManifestUpstream(context.Background(), codexModelsManifestRequest{
		url:               "https://models.example.test/v1/models?client_version=1.2.3",
		headers:           http.Header{"Authorization": []string{"Bearer test-only"}},
		useAPIKeyUpstream: true,
	}, "")

	require.NoError(t, err)
	require.NotEqual(t, `"upstream"`, manifest.ETag)
	require.Equal(t, `"upstream"`, manifest.upstreamETag)
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol"},{"slug":"vendor-new-preview"}]}`, string(manifest.Body))
	require.Equal(t, "1.2.3", upstream.request.URL.Query().Get("client_version"))
}

func TestAdjustAPIKeyCodexModelsManifest_DisablesResponsesLiteOnlyForKnown56Variants(t *testing.T) {
	body, err := adjustAPIKeyCodexModelsManifest([]byte(`{"models":[
		{"slug":"gpt-5.6-sol","use_responses_lite":true},
		{"slug":"vendor-new-preview","use_responses_lite":true}
	]}`))

	require.NoError(t, err)
	require.JSONEq(t, `{"models":[
		{"slug":"gpt-5.6-sol","use_responses_lite":false},
		{"slug":"vendor-new-preview","use_responses_lite":true}
	]}`, string(body))
}

func TestBuildCodexModelsManifestURL_PreservesBaseQueryAndAddsVersion(t *testing.T) {
	got, err := buildCodexModelsManifestURL("https://gateway.example.test/openai/v1?tenant=a", true, "1.2.3")

	require.NoError(t, err)
	require.Equal(t, "/openai/v1/models", got.Path)
	require.Equal(t, "a", got.Query().Get("tenant"))
	require.Equal(t, "1.2.3", got.Query().Get("client_version"))
}

func TestCodexModelsManifestETagMatches_HandlesWeakAndMultipleValues(t *testing.T) {
	require.True(t, codexModelsManifestETagMatches(`"old", W/"current"`, `"current"`))
	require.True(t, codexModelsManifestETagMatches("*", `"current"`))
	require.False(t, codexModelsManifestETagMatches(`"other"`, `"current"`))
}

func TestFilterCodexModelsManifest(t *testing.T) {
	blocked := func(slug string) bool { return strings.HasPrefix(slug, "blocked") }
	original := []byte(`{"models":[{"slug":"a"},{"no_slug":true}],"x":1}`)
	got, err := FilterCodexModelsManifest(original, blocked)
	require.NoError(t, err)
	require.Equal(t, string(original), string(got), "unchanged manifests keep their bytes")

	got, err = FilterCodexModelsManifest([]byte(`{"models":[{"slug":"blocked-1","Slug":"a"},{"slug":"blocked-2"}],"x":{"y":2}}`), blocked)
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[],"x":{"y":2}}`, string(got))

	for _, invalid := range []string{`{"models":null}`, `{"models":{}}`, `{"models":["a"]}`, `{"models":[{"slug":1}]}`, `{"models":[null]}`, `[]`} {
		_, err = FilterCodexModelsManifest([]byte(invalid), blocked)
		require.Error(t, err, invalid)
	}
}

func TestApplyCodexModelsDiscoveryIdentity(t *testing.T) {
	headers := http.Header{"User-Agent": {"curl/8.5.0"}, "X-Keep": {"1"}}
	ApplyCodexModelsDiscoveryIdentity(headers, "0.164.0-alpha.11.1")
	require.Equal(t, "codex_cli_rs/0.164.0-alpha.11.1", headers.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", headers.Get("Originator"))
	require.Equal(t, "1", headers.Get("X-Keep"))
	ApplyCodexModelsDiscoveryIdentity(headers, "0.164.0 (evil)")
	require.Equal(t, codexCLIUserAgent, headers.Get("User-Agent"))
}
