package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func (c *chatGPTSharedTurnCache) GetChatGPTUploadOwner(ctx context.Context, key string) (int64, error) {
	return c.ChatGPTTurnCache.(service.ChatGPTUploadCache).GetChatGPTUploadOwner(ctx, key)
}
func (c *chatGPTSharedTurnCache) ClaimChatGPTUploadOwner(ctx context.Context, key string, owner int64, ttl time.Duration) (int64, error) {
	return c.ChatGPTTurnCache.(service.ChatGPTUploadCache).ClaimChatGPTUploadOwner(ctx, key, owner, ttl)
}
func (c *chatGPTSharedTurnCache) BindChatGPTUploadOwner(ctx context.Context, keys []string, owner int64, ttl time.Duration) error {
	return c.ChatGPTTurnCache.(service.ChatGPTUploadCache).BindChatGPTUploadOwner(ctx, keys, owner, ttl)
}

func TestChatGPTNativeUploadPreservesBytesAndBindsSubsequentModelToOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	owner := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	var captured []struct {
		path, body, contentType string
		account                 string
	}
	const fileMetadata = `{"file_id":"file-TEST_ONLY_UPLOAD","upload_url":"/backend-api/estuary/upload_content_bytes?upload_url=TEST_ONLY_SIGNED_CAPABILITY","future":{"keep":true}}`
	const processing = "{\"event\":\"file.processing.file_ready\",\"file_id\":\"file-TEST_ONLY_UPLOAD\"}\n{\"event\":\"file.processing.completed\",\"file_id\":\"file-TEST_ONLY_UPLOAD\"}\n"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured = append(captured, struct {
			path, body, contentType string
			account                 string
		}{r.URL.RequestURI(), string(raw), r.Header.Get("Content-Type"), r.Header.Get("ChatGPT-Account-ID")})
		require.Equal(t, "Bearer TEST_ONLY_OAUTH", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Equal(t, []string{"first", "second"}, r.Header.Values("X-Desktop-Future"))
		switch r.URL.Path {
		case "/backend-api/files":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("test_error") != "" {
				_, _ = io.WriteString(w, `{"status":"error","error_code":"TEST_ONLY_LIMIT","future":true}`)
				return
			}
			_, _ = io.WriteString(w, fileMetadata)
		case "/backend-api/estuary/upload_content_bytes", "/api/estuary/upload_content_bytes":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"uploaded":true}`)
		case "/backend-api/files/process_upload_stream":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = io.WriteString(w, processing)
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n")
		case "/backend-api/conversation/TEST_ONLY_CONVERSATION":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"conversation_id":"TEST_ONLY_CONVERSATION","current_node":"final","mapping":{"user":{"parent":null,"message":{"id":"user","author":{"role":"user"}}},"final":{"parent":"user","message":{"id":"final","author":{"role":"assistant"},"status":"finished_successfully","end_turn":true,"content":{"content_type":"text","parts":["TEST_ONLY"]}}}}}`)
		case "/backend-api/files/download/file-TEST_ONLY_UPLOAD":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"download_url":"TEST_ONLY_DOWNLOAD","retry":false}`)
		default:
			t.Errorf("unexpected mock path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatUpdatesEnabled = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = provider.URL
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cache := &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	usage := &chatGPTLockedUsage{}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	defer billing.Stop()
	network := &chatGPTLocalNetwork{&http.Client{Transport: &http.Transport{Proxy: nil}}}
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: owner}, usage, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, billing, network, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, nil, billing, nil, nil, nil, nil, cfg, nil)
	h.chatGPTBillingSettings = &chatGPTPriceReader{price: .03}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		id := int64(23)
		if value := c.GetHeader("TEST-Key-ID"); value != "" {
			id, _ = strconv.ParseInt(value, 10, 64)
		}
		user := &service.User{ID: 21, Balance: 100}
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: id, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: user})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 21, Concurrency: 2})
	})
	router.POST("/chatgpt/backend-api/files", h.ChatGPTUpload)
	router.POST("/chatgpt/backend-api/files/process_upload_stream", h.ChatGPTUpload)
	router.POST("/chatgpt/backend-api/estuary/upload_content_bytes", h.ChatGPTUpload)
	router.POST("/chatgpt/api/estuary/upload_content_bytes", h.ChatGPTUpload)
	router.POST("/chatgpt/backend-api/f/conversation", h.ChatGPTConversation)
	router.GET("/chatgpt/backend-api/files/download/:file_id", h.ChatGPTFileDownload)
	request := func(method, path string, body []byte, contentType string, keyID int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("TEST-Key-ID", strconv.FormatInt(keyID, 10))
		r.Header.Set("User-Agent", "TEST_ONLY_Desktop")
		r.Header.Set("Cookie", "TEST_ONLY_CLIENT_COOKIE")
		r.Header.Add("X-Desktop-Future", "first")
		r.Header.Add("X-Desktop-Future", "second")
		router.ServeHTTP(w, r)
		return w
	}
	create := []byte(`{ "file_name":"TEST_ONLY.png","file_size":16,"use_case":"multimodal","future":null }`)
	w := request("POST", "/chatgpt/backend-api/files?x=a%2Fb&x=a+b", create, "application/json", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, fileMetadata, w.Body.String())
	require.Equal(t, string(create), captured[0].body)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "TEST_ONLY.png")
	require.NoError(t, err)
	image := []byte{'T', 'E', 'S', 'T', 0, 255, 13, 10}
	_, err = part.Write(image)
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("upload_url", "TEST_ONLY_SIGNED_CAPABILITY"))
	require.NoError(t, writer.Close())
	w = request("POST", "/chatgpt/backend-api/estuary/upload_content_bytes?x=a%2Fb&x=a+b", form.Bytes(), writer.FormDataContentType(), 23)
	require.Equal(t, 201, w.Code)
	require.Equal(t, form.String(), captured[1].body)
	require.Equal(t, writer.FormDataContentType(), captured[1].contentType)
	finish := []byte(`{"file_id":"file-TEST_ONLY_UPLOAD","use_case":"multimodal","future":{"keep":true}}`)
	w = request("POST", "/chatgpt/backend-api/files/process_upload_stream", finish, "application/json", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, processing, w.Body.String())
	require.True(t, w.Flushed)
	require.Equal(t, string(finish), captured[2].body)
	w = request("POST", "/chatgpt/api/estuary/upload_content_bytes?x=a%2Fb&x=a+b", form.Bytes(), writer.FormDataContentType(), 23)
	require.Equal(t, 201, w.Code)
	require.Equal(t, "/api/estuary/upload_content_bytes?x=a%2Fb&x=a+b", captured[3].path)
	require.Equal(t, form.String(), captured[3].body)
	w = request("POST", "/chatgpt/backend-api/files?test_error=true", create, "application/json", 23)
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"status":"error","error_code":"TEST_ONLY_LIMIT","future":true}`, w.Body.String())
	require.Zero(t, usage.count(), "upload is not a model turn or generated image")
	calls := len(captured)
	w = request("POST", "/chatgpt/backend-api/files/process_upload_stream", finish, "application/json", 24)
	require.Equal(t, 404, w.Code)
	require.Len(t, captured, calls, "another Key must never reach the provider")
	w = request("POST", "/chatgpt/backend-api/estuary/upload_content_bytes?upload_url=TEST_ONLY_DIFFERENT", form.Bytes(), writer.FormDataContentType(), 23)
	require.Equal(t, 400, w.Code)
	require.Len(t, captured, calls, "a changed capability must never reach the provider")
	model := []byte(`{"action":"next","model":"gpt-5-6-thinking","thinking_effort":"extended","messages":[{"id":"user","author":{"role":"user"},"content":{"content_type":"multimodal_text","parts":["TEST_ONLY describe",{"content_type":"image_asset_pointer","asset_pointer":"file-service://file-TEST_ONLY_UPLOAD"}]}}]}`)
	w = request("POST", "/chatgpt/backend-api/f/conversation", model, "application/json", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, string(model), captured[calls].body)
	require.Equal(t, "TEST_ONLY_ACCOUNT", captured[calls].account)
	require.Equal(t, 1, usage.count())
	require.Zero(t, usage.logs[0].ImageCount)
	require.InDelta(t, .03, usage.logs[0].ActualCost, .000001)
	w = request("GET", "/chatgpt/backend-api/files/download/file-TEST_ONLY_UPLOAD", nil, "", 23)
	require.Equal(t, 200, w.Code)
	w = request("GET", "/chatgpt/backend-api/files/download/file-TEST_ONLY_UPLOAD", nil, "", 24)
	require.Equal(t, 404, w.Code)
}

func TestChatGPTAttachmentOwnerRejectsMixedAndForeignFiles(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil)
	turns, err := svc.ChatGPTTurnCache()
	require.NoError(t, err)
	uploads, err := svc.ChatGPTUploadCache()
	require.NoError(t, err)
	scope := service.ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	require.NoError(t, uploads.BindChatGPTUploadOwner(ctx, []string{scope.UploadedFileKey("file_FIRST")}, 11, time.Minute))
	require.NoError(t, uploads.BindChatGPTUploadOwner(ctx, []string{scope.UploadedFileKey("file_SECOND")}, 12, time.Minute))
	_, err = h.chatGPTAttachmentOwner(ctx, scope, turns, []string{"file_FIRST", "file_SECOND"})
	require.Error(t, err)
	_, err = h.chatGPTAttachmentOwner(ctx, service.ChatGPTTurnScope{UserID: 1, APIKeyID: 4, GroupID: 3}, turns, []string{"file_FIRST"})
	require.Error(t, err)
}
