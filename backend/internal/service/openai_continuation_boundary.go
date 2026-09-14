package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

const openAIResponseIDContextKey = "openai_response_id_for_account_binding"

// OpenAIContinuationAccountID resolves the account that owns a client-visible
// continuation before scheduling can replace a stale sticky binding. A
// provider-issued response ID is accepted only with an explicit account
// binding; session stickiness cannot prove who issued an old response.
func (s *OpenAIGatewayService) OpenAIContinuationAccountID(ctx context.Context, groupID *int64, previousResponseID string) (int64, error) {
	if s == nil || strings.TrimSpace(previousResponseID) == "" {
		return 0, nil
	}
	if store := s.getOpenAIWSStateStore(); store != nil {
		return store.GetResponseAccount(ctx, derefGroupID(groupID), strings.TrimSpace(previousResponseID))
	}
	return 0, nil
}

func setOpenAIResponseIDForAccountBinding(c *gin.Context, responseID string) {
	if c != nil && strings.TrimSpace(responseID) != "" {
		c.Set(openAIResponseIDContextKey, strings.TrimSpace(responseID))
	}
}

func openAIResponseIDForAccountBinding(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.GetString(openAIResponseIDContextKey))
}

// OpenAIContinuationAccountMatches never guesses the owner of a provider
// response ID. An unknown or different owner requires a fresh conversation.
func OpenAIContinuationAccountMatches(previousResponseID string, ownerAccountID, selectedAccountID int64) bool {
	return strings.TrimSpace(previousResponseID) == "" ||
		(ownerAccountID > 0 && selectedAccountID == ownerAccountID)
}

func openAIWSAccountTurnStateSessionHash(apiKeyID, accountID int64, sessionHash string) string {
	if accountID <= 0 || strings.TrimSpace(sessionHash) == "" {
		return ""
	}
	return fmt.Sprintf("key:%d:account:%d:%s", apiKeyID, accountID, sessionHash)
}

func (s *OpenAIGatewayService) openAISessionHashForTurnState(c *gin.Context, promptCacheKey string) string {
	if hash := s.GenerateSessionHash(c, nil); hash != "" {
		return hash
	}
	hash, _ := openAIWSSessionHashesFromID(promptCacheKey)
	return hash
}

// resolveOpenAIWSTurnStateForAccount accepts a client-supplied state token for
// OAuth only when this Gateway has observed it for the selected account. A
// token from a previous account is never forwarded after a pool switch.
func (s *OpenAIGatewayService) resolveOpenAIWSTurnStateForAccount(account *Account, groupID, apiKeyID int64, sessionHash, incoming string) string {
	incoming = strings.TrimSpace(incoming)
	if s == nil || account == nil || sessionHash == "" {
		if account != nil && account.Type == AccountTypeOAuth {
			return ""
		}
		return incoming
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		if account.Type == AccountTypeOAuth {
			return ""
		}
		return incoming
	}
	stored, ok := store.GetSessionTurnState(groupID, openAIWSAccountTurnStateSessionHash(apiKeyID, account.ID, sessionHash))
	if account.Type != AccountTypeOAuth {
		if incoming != "" {
			return incoming
		}
		if ok {
			return stored
		}
		return ""
	}
	if ok && incoming != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(incoming)) == 1 {
		return incoming
	}
	if ok {
		return stored
	}
	return ""
}
