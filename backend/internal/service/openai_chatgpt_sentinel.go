package service

import (
	"errors"
	"net/http"
	"time"
)

// Windows Desktop gets a Sentinel prepare token from one POST and sends it
// back on f/conversation. The provider rejects a token another account issued
// ("Unusual activity"), so the token is bound to the account that issued it.
// Only its scoped digest and the account ID are cached.
const (
	ChatGPTSentinelPreparePath        = "/chatgpt/backend-api/sentinel/chat-requirements/prepare"
	ChatGPTSentinelPrepareTokenHeader = "Openai-Sentinel-Chat-Requirements-Prepare-Token"
	// Covers a token prepared while the user is still typing.
	ChatGPTSentinelTTL = 30 * time.Minute
	// The observed response is ~100 KiB of proof-of-work and turnstile data.
	MaxChatGPTSentinelResponseBytes = 1024 * 1024
)

func (s ChatGPTTurnScope) SentinelKey(token string) string {
	return s.uploadKey("sentinel", token)
}

func ChatGPTSentinelPrepareResponse(raw []byte) (string, error) {
	return chatGPTAttestationField(raw, "prepare_token", true)
}

// ChatGPTRequestSentinelToken returns "" when the request carries no token.
// Repeated headers must agree; the opaque value is never trimmed or split.
func ChatGPTRequestSentinelToken(headers http.Header) (string, error) {
	token := ""
	for _, value := range headers.Values(ChatGPTSentinelPrepareTokenHeader) {
		if !validChatGPTAttestation(value) || (token != "" && value != token) {
			return "", errors.New("invalid native Chat sentinel token")
		}
		token = value
	}
	return token, nil
}
