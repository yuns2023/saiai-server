package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	clientDeviceEnrollMaxBody      = 4 << 10
	clientDeviceMaxVersionLength   = 64
	clientDeviceMaxPlatformLength  = 32
	clientDeviceEnrollStatusActive = "active"
	clientDeviceEnrollCodeInvalid  = "invalid_request"
	clientDeviceEnrollCodeInternal = "internal_error"
	clientDeviceEnrollCodeRevoked  = "client_device_revoked"
	clientDeviceEnrollCodeLimit    = "client_device_limit"
	clientDeviceEnrollCodeBusy     = "client_device_busy"
	clientDeviceEnrollCodeAuth     = "unauthorized"
)

type clientDeviceEnrollRequest struct {
	PublicKey     string `json:"public_key"`
	ClientVersion string `json:"client_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

// ClientDeviceEnrollResponse is the 200 body of POST /api/v1/client/devices/enroll.
type ClientDeviceEnrollResponse struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
}

type clientDeviceEnrollError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// EnrollDevice registers the local proxy's device key for the authenticated
// API key's user (docs/CLIENT_PROOF_V1.md §2). The wire bodies are the flat
// shapes defined by the contract, not the admin response envelope.
// POST /api/v1/client/devices/enroll
func (h *ClientHandler) EnrollDevice(c *gin.Context) {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, clientDeviceEnrollError{Code: clientDeviceEnrollCodeAuth})
		return
	}
	if h == nil || h.clientDevices == nil {
		c.JSON(http.StatusServiceUnavailable, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInternal})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, clientDeviceEnrollMaxBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInvalid, Message: "request body too large or unreadable"})
		return
	}
	var req clientDeviceEnrollRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInvalid, Message: "invalid JSON body"})
		return
	}
	if req.PublicKey == "" ||
		!utf8.ValidString(req.ClientVersion) || utf8.RuneCountInString(req.ClientVersion) > clientDeviceMaxVersionLength ||
		!utf8.ValidString(req.OS) || utf8.RuneCountInString(req.OS) > clientDeviceMaxPlatformLength ||
		!utf8.ValidString(req.Arch) || utf8.RuneCountInString(req.Arch) > clientDeviceMaxPlatformLength {
		c.JSON(http.StatusBadRequest, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInvalid, Message: "invalid enrollment fields"})
		return
	}
	userID := apiKey.UserID
	if apiKey.User != nil && apiKey.User.ID > 0 {
		userID = apiKey.User.ID
	}
	device, err := h.clientDevices.Enroll(c.Request.Context(), userID, apiKey.ID, req.PublicKey, req.ClientVersion, req.OS, req.Arch)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, ClientDeviceEnrollResponse{DeviceID: device.DeviceID, Status: clientDeviceEnrollStatusActive})
	case errors.Is(err, service.ErrClientDeviceInvalid):
		c.JSON(http.StatusBadRequest, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInvalid, Message: "public_key must be base64url of a 65-byte uncompressed P-256 point"})
	case errors.Is(err, service.ErrClientDeviceRevoked):
		c.JSON(http.StatusForbidden, clientDeviceEnrollError{Code: clientDeviceEnrollCodeRevoked})
	case errors.Is(err, service.ErrClientDeviceLimit):
		c.JSON(http.StatusTooManyRequests, clientDeviceEnrollError{Code: clientDeviceEnrollCodeLimit})
	case errors.Is(err, service.ErrClientDeviceBusy):
		// Concurrency cap or per-user rate limit: the client backs off on 5xx.
		c.Header("Retry-After", strconv.Itoa(service.ClientDeviceEnrollRetryAfter))
		c.JSON(http.StatusServiceUnavailable, clientDeviceEnrollError{Code: clientDeviceEnrollCodeBusy})
	default:
		slog.Error("client_device_enroll_failed", "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, clientDeviceEnrollError{Code: clientDeviceEnrollCodeInternal})
	}
}
