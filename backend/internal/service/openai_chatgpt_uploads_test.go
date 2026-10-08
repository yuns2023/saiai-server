package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatGPTUploadOwnershipIsScopedImmutableAndContentHiding(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache().(ChatGPTUploadCache)
	scope := ChatGPTTurnScope{1, 2, 3}
	keys, err := ChatGPTUploadResponseKeys(scope, []byte(`{"file_id":"file-TEST_ONLY","upload_url":"/backend-api/estuary/upload_content_bytes?upload_url=TEST_ONLY_SIGNED_CAPABILITY"}`))
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, key := range keys {
		require.NotContains(t, key, "TEST_ONLY")
	}
	require.NoError(t, cache.BindChatGPTUploadOwner(ctx, keys, 11, time.Minute))
	require.Error(t, cache.BindChatGPTUploadOwner(ctx, keys, 12, time.Minute))
	owner, err := cache.GetChatGPTUploadOwner(ctx, scope.UploadedFileKey("file-TEST_ONLY"))
	require.NoError(t, err)
	require.Equal(t, int64(11), owner)
	for _, other := range []ChatGPTTurnScope{{4, 2, 3}, {1, 4, 3}, {1, 2, 4}} {
		owner, err = cache.GetChatGPTUploadOwner(ctx, other.UploadedFileKey("file-TEST_ONLY"))
		require.NoError(t, err)
		require.Zero(t, owner)
	}
	first, err := cache.ClaimChatGPTUploadOwner(ctx, scope.UploadSessionKey("TEST_ONLY_DEVICE"), 11, time.Minute)
	require.NoError(t, err)
	next, err := cache.ClaimChatGPTUploadOwner(ctx, scope.UploadSessionKey("TEST_ONLY_DEVICE"), 12, time.Minute)
	require.NoError(t, err)
	require.Equal(t, first, next)
}

func TestChatGPTUploadMetadataRejectsForeignEstuaryAndInvalidIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"file_id":"file-TEST_ONLY","upload_url":"https://foreign.example/backend-api/estuary/upload_content_bytes?upload_url=TEST_ONLY"}`,
		`{"file_id":"file-TEST_ONLY","upload_url":"/backend-api/estuary/upload_content_bytes"}`,
		`{"file_id":"file-TEST_ONLY","upload_url":"/backend-api/estuary/upload_content_bytes?upload_url=a&upload_url=b"}`,
		`{"file_id":"../file-TEST_ONLY","upload_url":"https://blob.example/upload?sig=TEST_ONLY"}`,
		`{"file_id":"file-TEST_ONLY","upload_url":"http://blob.example/upload?sig=TEST_ONLY"}`,
	} {
		_, err := ChatGPTUploadResponseKeys(ChatGPTTurnScope{1, 2, 3}, []byte(raw))
		require.Error(t, err)
	}
}

func TestChatGPTUploadMetadataKeepsNativeErrorsAndDirectBlobOwnership(t *testing.T) {
	scope := ChatGPTTurnScope{1, 2, 3}
	keys, err := ChatGPTUploadResponseKeys(scope, []byte(`{"status":"error","error_code":"TEST_ONLY_LIMIT"}`))
	require.NoError(t, err)
	require.Empty(t, keys)
	keys, err = ChatGPTUploadResponseKeys(scope, []byte(`{"file_id":"file-TEST_ONLY","upload_url":"https://blob.example/upload?sig=TEST_ONLY_CAPABILITY"}`))
	require.NoError(t, err)
	require.Equal(t, []string{scope.UploadedFileKey("file-TEST_ONLY")}, keys)
	keys, err = ChatGPTUploadResponseKeys(scope, []byte(`{"file_id":"file-TEST_ONLY","upload_url":"/api/estuary/upload_content_bytes?upload_url=TEST_ONLY_CAPABILITY"}`))
	require.NoError(t, err)
	require.Equal(t, []string{scope.UploadedFileKey("file-TEST_ONLY"), scope.UploadURLKey("TEST_ONLY_CAPABILITY")}, keys)
}

func TestChatGPTAttachmentInspectionUsesOnlyNewUserAndExplicitPointers(t *testing.T) {
	raw := `{"messages":[{"author":{"role":"user"},"content":{"parts":[{"content_type":"image_asset_pointer","asset_pointer":"file-service://file-OLD"}]}},{"author":{"role":"user"},"content":{"parts":["TEST_ONLY arbitrary file-FOREIGN text",{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_TEST_ONLY"}]},"metadata":{"attachments":[{"id":"file_TEST_ONLY"},{"file_id":"file-OTHER"}]}}]}`
	ids, err := ChatGPTRequestUploadFileIDs([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, []string{"file_TEST_ONLY", "file-OTHER"}, ids)
	_, err = ChatGPTRequestUploadFileIDs([]byte(strings.Replace(raw, "sediment://file_TEST_ONLY", "https://foreign.example/file_TEST_ONLY", 1)))
	require.Error(t, err)
}
