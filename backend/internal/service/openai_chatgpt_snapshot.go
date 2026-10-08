package service

import (
	"encoding/json"
	"errors"
)

const MaxChatGPTConversationSnapshotBytes = 8 * 1024 * 1024

// ChatGPTSnapshotInspection separates file ownership from billable completion.
// Outcome is a fixed diagnostic enum. No pointers or message content escape.
type ChatGPTSnapshotInspection struct {
	Summary        ChatGPTConversationStreamSummary
	Images         ChatGPTImageEvidence
	DeliveryImages ChatGPTImageEvidence
	Completed      bool
	Outcome        string
}

func InspectChatGPTConversationSnapshot(raw []byte, conversationID, userMessageHash string) (ChatGPTConversationStreamSummary, ChatGPTImageEvidence, bool, error) {
	result, err := InspectChatGPTConversationDelivery(raw, conversationID, userMessageHash)
	return result.Summary, result.Images, result.Completed, err
}

// InspectChatGPTConversationDelivery verifies the current branch against the
// original user-message digest before exposing any asset ownership. Preview
// delivery does not establish successful completion or add images to a bill.
func InspectChatGPTConversationDelivery(raw []byte, conversationID, userMessageHash string) (ChatGPTSnapshotInspection, error) {
	result := ChatGPTSnapshotInspection{Outcome: "invalid_snapshot"}
	if len(raw) > MaxChatGPTConversationSnapshotBytes || userMessageHash == "" {
		return result, errors.New("native Chat snapshot cannot establish turn ownership")
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
		return result, errors.New("invalid native Chat snapshot")
	}
	if (snapshot.ID != "" && snapshot.ID != conversationID) ||
		(snapshot.ConversationID != "" && snapshot.ConversationID != conversationID) ||
		(snapshot.ID == "" && snapshot.ConversationID == "") {
		return result, errors.New("native Chat snapshot identity mismatch")
	}
	result.Summary.ConversationID = conversationID
	if snapshot.AsyncStatus != nil {
		result.Summary.AsyncStatusSeen, result.Summary.AsyncStatus = true, *snapshot.AsyncStatus
	}
	branch := make([]json.RawMessage, 0, 16)
	visited := make(map[string]bool)
	id := snapshot.CurrentNode
	leafAssistant, leafImageTool := false, false
	plainAssistantBranch, leafText := true, false
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
				Name string `json:"name"`
			} `json:"author"`
			Status  string `json:"status"`
			EndTurn *bool  `json:"end_turn"`
			Content struct {
				Type string `json:"content_type"`
			} `json:"content"`
			Metadata struct {
				ModelSlug string `json:"model_slug"`
				IsError   bool   `json:"is_error"`
			} `json:"metadata"`
		}
		if json.Unmarshal(node.Message, &message) != nil || message.ID != id {
			break
		}
		if message.Metadata.IsError || message.Status == "finished_failed" {
			result.Outcome = "failed_message"
			return result, nil
		}
		if len(branch) == 0 {
			leafAssistant = message.Author.Role == "assistant" && message.Status == "finished_successfully" && message.EndTurn != nil && *message.EndTurn
			leafImageTool = message.Author.Role == "tool" && chatGPTImageTool(message.Author.Name) && message.Status == "finished_successfully"
			leafText = message.Content.Type == "text"
		}
		if message.Author.Role == "user" {
			if ChatGPTMessageIDHash(message.ID) != userMessageHash {
				break
			}
			var images chatGPTImageObserver
			delivery := chatGPTImageObserver{deliveryOnly: true}
			for i := len(branch) - 1; i >= 0; i-- {
				images.observeMessage(branch[i])
				delivery.observeMessage(branch[i])
			}
			result.Images, result.DeliveryImages = images.evidence(), delivery.evidence()
			result.Summary.ImageGenerationSeen, result.Summary.ImageCount = result.Images.GenerationSeen, len(result.Images.AssetHashes)
			result.Outcome = "async_pending"
			if snapshot.AsyncStatus == nil {
				// Synchronous text turns omit async_status. A verified current
				// branch ending in a successful end_turn assistant text message
				// is sufficient, provided no tool or image work occurred. Missing
				// status on an image/tool branch must still defer settlement.
				if leafAssistant && leafText && plainAssistantBranch && !result.Images.GenerationSeen && !result.DeliveryImages.GenerationSeen {
					result.Completed, result.Summary.CompletionSeen = true, true
					result.Outcome = "completed_synchronous_assistant"
				}
				return result, nil
			}
			if ChatGPTAsyncPending(*snapshot.AsyncStatus) {
				return result, nil
			}
			result.Outcome = "leaf_not_terminal"
			// Native image turns may end at the image tool itself. Require an
			// inactive provider state and a completed image on that leaf; an
			// older ancestor image or an arbitrary tool is insufficient.
			var leafImages chatGPTImageObserver
			if leafImageTool && len(branch) > 0 {
				leafImages.observeMessage(branch[0])
			}
			if !leafAssistant && len(leafImages.evidence().AssetHashes) == 0 {
				return result, nil
			}
			result.Completed, result.Summary.CompletionSeen = true, true
			result.Outcome = "completed_assistant"
			if !leafAssistant {
				result.Outcome = "completed_image_tool"
			}
			return result, nil
		}
		if result.Summary.ObservedModel == "" && message.Author.Role == "assistant" && ValidChatGPTMetadataValue(message.Metadata.ModelSlug, 100) {
			result.Summary.ObservedModel = message.Metadata.ModelSlug
		}
		plainAssistantBranch = plainAssistantBranch && message.Author.Role == "assistant"
		branch = append(branch, node.Message)
		if node.Parent == nil {
			break
		}
		id = *node.Parent
	}
	result.Outcome = "branch_not_owned"
	return result, errors.New("native Chat snapshot branch does not belong to the pending message")
}
