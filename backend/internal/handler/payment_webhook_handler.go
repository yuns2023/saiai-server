package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
)

const maxPaymentWebhookBodySize = 64 << 10

type PaymentWebhookHandler struct {
	paymentService *service.PaymentService
}

func NewPaymentWebhookHandler(paymentService *service.PaymentService) *PaymentWebhookHandler {
	return &PaymentWebhookHandler{paymentService: paymentService}
}

// Notify accepts provider callbacks through the adapter registry. The body is
// bounded before adapter parsing and always verified against the order snapshot.
func (h *PaymentWebhookHandler) Notify(c *gin.Context) {
	providerKey := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	definition, registered := provider.GetDefinition(providerKey)
	if !registered {
		c.String(http.StatusNotFound, "unsupported provider")
		return
	}
	successBody, failureBody := definition.WebhookSuccessBody, definition.WebhookFailureBody
	if successBody == "" {
		successBody = "success"
	}
	if failureBody == "" {
		failureBody = "fail"
	}
	rawBody := c.Request.URL.RawQuery
	if c.Request.Method != http.MethodGet {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxPaymentWebhookBodySize+1))
		if err != nil || len(body) > maxPaymentWebhookBodySize {
			c.String(http.StatusBadRequest, failureBody)
			return
		}
		rawBody = string(body)
	}
	headers := make(map[string]string, len(c.Request.Header))
	for key := range c.Request.Header {
		headers[strings.ToLower(key)] = c.GetHeader(key)
	}
	err := h.paymentService.HandlePaymentNotification(c.Request.Context(), providerKey, rawBody, headers)
	if err == nil || errors.Is(err, service.ErrPaymentOrderNotFound) {
		if errors.Is(err, service.ErrPaymentOrderNotFound) {
			slog.Warn("unknown payment order webhook acknowledged", "provider", providerKey)
		}
		c.String(http.StatusOK, successBody)
		return
	}
	secretKeys := append(provider.SecretConfigKeys(), "pid")
	slog.Error("payment webhook rejected", "provider", providerKey, "error", logredact.RedactText(err.Error(), secretKeys...))
	c.String(http.StatusBadRequest, failureBody)
}
