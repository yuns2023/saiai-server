package repository

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstream_NativeCodexPreservesWireAndRejectsImplicitRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		for _, compressed := range []bool{false, true} {
			t.Run(http.StatusText(status)+map[bool]string{false: "/json", true: "/zstd"}[compressed], func(t *testing.T) {
				wire := []byte(`{ "model":"mock_model", "reasoning":{"effort":"high"}, "unknown":[null,true] }`)
				if compressed {
					encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
					require.NoError(t, err)
					wire = encoder.EncodeAll(wire, nil)
					encoder.Close()
				}
				var redirects atomic.Int32
				type observedRequest struct {
					method, query string
					body          []byte
					headers       http.Header
				}
				seen := make(chan observedRequest, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/target" {
						redirects.Add(1)
						w.WriteHeader(http.StatusOK)
						return
					}
					body, _ := io.ReadAll(r.Body)
					seen <- observedRequest{r.Method, r.URL.RawQuery, body, r.Header.Clone()}
					w.Header().Set("Location", "/target")
					w.WriteHeader(status)
				}))
				defer server.Close()
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowPrivateHosts = true
				upstream := NewHTTPUpstream(cfg)
				request, err := http.NewRequest(http.MethodPost, server.URL+"/responses?dup=a%2Fb&dup=a+b&flag", bytes.NewReader(wire))
				require.NoError(t, err)
				request.Header.Set("User-Agent", "codex_cli_rs/0.159.2")
				request.Header["X-Codex-Future"] = []string{"first", "second"}
				if compressed {
					request.Header.Set("Content-Encoding", "zstd")
				}
				request = request.WithContext(service.WithNativeCodexRedirectPolicy(request.Context()))
				response, err := upstream.Do(request, "", 999, 1)
				require.NoError(t, err)
				require.Equal(t, status, response.StatusCode)
				require.NoError(t, response.Body.Close())
				require.Zero(t, redirects.Load(), "no redirected application request may be issued")
				observed := <-seen
				require.Equal(t, http.MethodPost, observed.method)
				require.Equal(t, wire, observed.body)
				require.Equal(t, request.URL.RawQuery, observed.query)
				require.Equal(t, []string{"first", "second"}, observed.headers.Values("X-Codex-Future"))
				require.Equal(t, request.Header.Get("User-Agent"), observed.headers.Get("User-Agent"))
				// A following compatibility request uses the very same pooled
				// client, proving the native policy did not mutate shared state.
				legacy, err := http.NewRequest(http.MethodPost, server.URL+"/responses", bytes.NewReader(wire))
				require.NoError(t, err)
				response, err = upstream.Do(legacy, "", 999, 1)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.NoError(t, response.Body.Close())
				require.Equal(t, int32(1), redirects.Load())
			})
		}
	}
}
