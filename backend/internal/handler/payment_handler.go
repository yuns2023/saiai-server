package handler

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PaymentHandler exposes authenticated native balance and subscription payments.
type PaymentHandler struct {
	paymentService *service.PaymentService
	configService  *service.PaymentConfigService
}

func NewPaymentHandler(paymentService *service.PaymentService, configService *service.PaymentConfigService) *PaymentHandler {
	return &PaymentHandler{paymentService: paymentService, configService: configService}
}

type createPaymentOrderRequest struct {
	Amount             float64 `json:"amount"`
	OrderType          string  `json:"order_type"`
	PlanID             int64   `json:"plan_id"`
	ProviderInstanceID int64   `json:"provider_instance_id"`
	PaymentType        string  `json:"payment_type" binding:"required"`
	IsMobile           *bool   `json:"is_mobile,omitempty"`
}

func (h *PaymentHandler) ListPlans(c *gin.Context) {
	plans, err := h.configService.ListPlans(c.Request.Context(), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, plans)
}

func (h *PaymentHandler) GetConfig(c *gin.Context) {
	cfg, err := h.configService.GetPaymentConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	paymentMethods := []service.PaymentMethodResponse{}
	if cfg.Enabled {
		paymentMethods, err = h.configService.GetAvailablePaymentMethods(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}
	response.Success(c, gin.H{"config": cfg, "payment_methods": paymentMethods})
}

func (h *PaymentHandler) CreateOrder(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req createPaymentOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	isMobile := strings.Contains(strings.ToLower(c.GetHeader("User-Agent")), "mobile")
	if req.IsMobile != nil {
		isMobile = *req.IsMobile
	}
	result, err := h.paymentService.CreateOrder(c.Request.Context(), service.CreatePaymentOrderRequest{
		UserID: subject.UserID, Amount: req.Amount, OrderType: req.OrderType, PlanID: req.PlanID,
		ProviderInstanceID: req.ProviderInstanceID, PaymentType: req.PaymentType,
		ClientIP: c.ClientIP(), IsMobile: isMobile,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, result)
}

func (h *PaymentHandler) ListMyOrders(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := response.ParsePagination(c)
	orders, total, err := h.paymentService.ListUserOrders(c.Request.Context(), subject.UserID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, orders, int64(total), page, pageSize)
}

func (h *PaymentHandler) GetOrder(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		response.BadRequest(c, "Invalid payment order ID")
		return
	}
	order, err := h.paymentService.GetUserOrder(c.Request.Context(), subject.UserID, orderID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, order)
}

func (h *PaymentHandler) CancelOrder(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		response.BadRequest(c, "Invalid payment order ID")
		return
	}
	if err := h.paymentService.CancelUserOrder(c.Request.Context(), subject.UserID, orderID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "payment order cancelled"})
}
