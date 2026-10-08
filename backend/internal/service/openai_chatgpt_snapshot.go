package service

import (
	"encoding/json"
	"errors"
)

const MaxChatGPTConversationSnapshotBytes = 8 * 1024 * 1024

// InspectChatGPTConversationSnapshot verifies the current completed branch,
// not an arbitrary historical assistant response. Only metadata and image
// digests escape this function; callers relay the original JSON bytes.
func InspectChatGPTConversationSnapshot(raw []byte, conversationID, userMessageHash string) (ChatGPTConversationStreamSummary, ChatGPTImageEvidence, bool, error) {
	var summary ChatGPTConversationStreamSummary
	var images chatGPTImageObserver
	if len(raw) > MaxChatGPTConversationSnapshotBytes || userMessageHash == "" {
		return summary, images.evidence(), false, errors.New("native Chat snapshot cannot establish turn ownership")
	}
	var snapshot struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversation_id"`
		AsyncStatus    *int   `json:"async_status"`
		CurrentNode    string `json:"current_node"`
		Mapping        map[string]struct {
			Parent  *string         `json:"parent"`
			Message json.RawMessage `json:"message"`
		} `json:"mapping"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Mapping) > 4096 {
		return summary, images.evidence(), false, errors.New("invalid native Chat snapshot")
	}
	if (snapshot.ID != "" && snapshot.ID != conversationID) ||
		(snapshot.ConversationID != "" && snapshot.ConversationID != conversationID) ||
		(snapshot.ID == "" && snapshot.ConversationID == "") {
		return summary, images.evidence(), false, errors.New("native Chat snapshot identity mismatch")
	}
	summary.ConversationID = conversationID
	if snapshot.AsyncStatus == nil || ChatGPTAsyncPending(*snapshot.AsyncStatus) {
		return summary, images.evidence(), false, nil
	}
	summary.AsyncStatusSeen, summary.AsyncStatus = true, *snapshot.AsyncStatus
	branch := make([]json.RawMessage, 0, 16)
	visited := make(map[string]bool)
	id := snapshot.CurrentNode
	for len(branch) < 256 && id != "" && !visited[id] {
		visited[id] = true
		node, exists := snapshot.Mapping[id]
		if !exists {
			break
		}
		var message struct {
			ID     string `json:"id"`
			Author struct {
				Role string `json:"role"`
			} `json:"author"`
			Status   string `json:"status"`
			EndTurn  *bool  `json:"end_turn"`
			Metadata struct {
				ModelSlug string `json:"model_slug"`
				IsError   bool   `json:"is_error"`
			} `json:"metadata"`
		}
		if json.Unmarshal(node.Message, &message) != nil || message.ID != id {
			break
		}
		if len(branch) == 0 && (message.Author.Role != "assistant" || message.Status != "finished_successfully" || message.EndTurn == nil || !*message.EndTurn || message.Metadata.IsError) {
			return summary, images.evidence(), false, nil
		}
		if message.Author.Role == "user" {
			if ChatGPTMessageIDHash(message.ID) != userMessageHash {
				break
			}
			for i := len(branch) - 1; i >= 0; i-- {
				images.observeMessage(branch[i])
			}
			evidence := images.evidence()
			summary.CompletionSeen = true
			summary.ImageGenerationSeen, summary.ImageCount = evidence.GenerationSeen, len(evidence.AssetHashes)
			return summary, evidence, true, nil
		}
		if message.Metadata.IsError || message.Status == "finished_failed" {
			return summary, images.evidence(), false, nil
		}
		if summary.ObservedModel == "" && message.Author.Role == "assistant" && ValidChatGPTMetadataValue(message.Metadata.ModelSlug, 100) {
			summary.ObservedModel = message.Metadata.ModelSlug
		}
		branch = append(branch, node.Message)
		if node.Parent == nil {
			break
		}
		id = *node.Parent
	}
	return summary, images.evidence(), false, errors.New("native Chat snapshot branch does not belong to the pending message")
}
