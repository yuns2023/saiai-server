package chain

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeRPC is a minimal JSON-RPC server; handler returns (result, error).
type fakeRPC struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []string
	ua      []string
	handler func(method string, params json.RawMessage) (any, *RPCError)
}

func newFakeRPC(t *testing.T, h func(method string, params json.RawMessage) (any, *RPCError)) (*fakeRPC, *httptest.Server) {
	f := &fakeRPC{t: t, handler: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		f.ua = append(f.ua, r.Header.Get("User-Agent"))
		f.mu.Unlock()
		res, rerr := f.handler(req.Method, req.Params)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = res
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeRPC) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}
