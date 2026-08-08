package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type PaymentHandler struct {
	paymentService *service.PaymentService
	configService  *service.PaymentConfigService
}

func NewPaymentHandler(paymentService *service.PaymentService, configService *service.PaymentConfigService) *PaymentHandler {
	return &PaymentHandler{paymentService: paymentService, configService: configService}
}

func (h *PaymentHandler) GetConfig(c *gin.Context) {
	cfg, err := h.configService.GetPaymentConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *PaymentHandler) UpdateConfig(c *gin.Context) {
	var req service.UpdatePaymentConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.configService.UpdatePaymentConfig(c.Request.Context(), req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.GetConfig(c)
}

func (h *PaymentHandler) ListProviders(c *gin.Context) {
	providers, err := h.configService.ListProviderInstances(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, providers)
}

func (h *PaymentHandler) ListProviderDefinitions(c *gin.Context) {
	response.Success(c, h.configService.ListProviderDefinitions())
}

func (h *PaymentHandler) CreateProvider(c *gin.Context) {
	var req service.CreatePaymentProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	provider, err := h.configService.CreateProviderInstance(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, provider)
}

func (h *PaymentHandler) UpdateProvider(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	var req service.UpdatePaymentProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	provider, err := h.configService.UpdateProviderInstance(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, provider)
}

func (h *PaymentHandler) DeleteProvider(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	if err := h.configService.DeleteProviderInstance(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "payment provider deleted"})
}

func (h *PaymentHandler) ListPlans(c *gin.Context) {
	plans, err := h.configService.ListPlans(c.Request.Context(), false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, plans)
}

func (h *PaymentHandler) CreatePlan(c *gin.Context) {
	var req service.CreatePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	plan, err := h.configService.CreatePlan(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, plan)
}

func (h *PaymentHandler) UpdatePlan(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	var req service.UpdatePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	plan, err := h.configService.UpdatePlan(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, plan)
}

func (h *PaymentHandler) DeletePlan(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	if err := h.configService.DeletePlan(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "subscription plan deleted"})
}

func (h *PaymentHandler) ListOrders(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	orders, total, err := h.paymentService.ListAdminOrders(c.Request.Context(), pageSize, (page-1)*pageSize, c.Query("status"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, orders, int64(total), page, pageSize)
}

func (h *PaymentHandler) RetryFulfillment(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	if err := h.paymentService.RetryFulfillment(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "payment fulfillment retried"})
}

func (h *PaymentHandler) RequestRefund(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	var req service.RequestPaymentRefundInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	req.Operator = paymentAdminOperator(c)
	order, err := h.paymentService.RequestRefund(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, order)
}

func (h *PaymentHandler) ResolveRefund(c *gin.Context) {
	id, ok := paymentIDParam(c)
	if !ok {
		return
	}
	var req service.ResolvePaymentRefundInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	req.Operator = paymentAdminOperator(c)
	order, err := h.paymentService.ResolveRefund(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, order)
}

func paymentAdminOperator(c *gin.Context) string {
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		return "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	return "admin:unknown"
}

func paymentIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid payment ID")
		return 0, false
	}
	return id, true
}
