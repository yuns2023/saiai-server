package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// opsCtxAwareSettingsStub fails reads on a canceled context, like the real
// database-backed repository does.
type opsCtxAwareSettingsStub struct {
	opsLogFilterSettingsStub
}

func (repo *opsCtxAwareSettingsStub) GetValue(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return repo.opsLogFilterSettingsStub.GetValue(ctx, key)
}

func TestOpsErrorLogger_ClientCanceledUpstreamHonorsIgnoreSetting(test *testing.T) {
	gin.SetMode(gin.TestMode)
	const canceledMessage = `Post "https://api.anthropic.com/v1/messages?beta=true": context canceled`
	for _, testCase := range []struct {
		name            string
		ignore          bool
		cancelRequest   bool
		upstreamMessage string
		priorFailures   []string
		logged          bool
	}{
		{name: "client hung up, ignore on", ignore: true, cancelRequest: true, upstreamMessage: canceledMessage, logged: false},
		{name: "client hung up, ignore off", ignore: false, cancelRequest: true, upstreamMessage: canceledMessage, logged: true},
		{name: "real upstream error then client hung up", ignore: true, cancelRequest: true, upstreamMessage: `Post "https://api.anthropic.com/v1/messages": write: connection reset by peer`, logged: true},
		{name: "upstream canceled while client still connected", ignore: true, cancelRequest: false, upstreamMessage: canceledMessage, logged: true},
		{name: "client hung up on the only attempt, recorded as an event", ignore: true, cancelRequest: true, upstreamMessage: canceledMessage, priorFailures: []string{canceledMessage}, logged: false},
		{name: "real 503 before failover, then client hung up", ignore: true, cancelRequest: true, upstreamMessage: canceledMessage, priorFailures: []string{"upstream returned 503 Service Unavailable", canceledMessage}, logged: true},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			prepareOpsLogFilterTestQueue(test)
			advanced := `{"ignore_context_canceled":false}`
			if testCase.ignore {
				advanced = `{"ignore_context_canceled":true}`
			}
			repo := &opsCtxAwareSettingsStub{opsLogFilterSettingsStub{values: map[string]string{
				service.SettingKeyOpsAdvancedSettings: advanced,
			}}}
			ops := service.NewOpsService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)

			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/messages", func(ctx *gin.Context) {
				if testCase.cancelRequest {
					cancel()
				}
				if len(testCase.priorFailures) > 0 {
					events := make([]*service.OpsUpstreamErrorEvent, 0, len(testCase.priorFailures))
					for _, message := range testCase.priorFailures {
						events = append(events, &service.OpsUpstreamErrorEvent{Platform: service.PlatformAnthropic, Kind: "request_error", Message: message})
					}
					ctx.Set(service.OpsUpstreamErrorsKey, events)
				}
				service.SetOpsUpstreamError(ctx, 0, testCase.upstreamMessage, "")
				ctx.JSON(http.StatusBadGateway, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream request failed"}})
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestCtx)
			router.ServeHTTP(httptest.NewRecorder(), request)

			if testCase.logged {
				require.Equal(test, int64(1), OpsErrorLogEnqueuedTotal())
				return
			}
			require.Zero(test, OpsErrorLogEnqueuedTotal())
		})
	}
}

func TestOpsErrorLogger_MonitoringSwitchReadSurvivesClientCancel(test *testing.T) {
	gin.SetMode(gin.TestMode)
	prepareOpsLogFilterTestQueue(test)
	repo := &opsCtxAwareSettingsStub{opsLogFilterSettingsStub{values: map[string]string{
		service.SettingKeyOpsMonitoringEnabled: "false",
	}}}
	ops := service.NewOpsService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(ctx *gin.Context) {
		cancel()
		ctx.JSON(http.StatusBadGateway, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream request failed"}})
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestCtx))
	require.Zero(test, OpsErrorLogEnqueuedTotal())
}

// opsBlockingSettingsStub, once armed, never answers until the caller gives up.
type opsBlockingSettingsStub struct {
	opsLogFilterSettingsStub
	armed bool
}

func (repo *opsBlockingSettingsStub) GetValue(ctx context.Context, key string) (string, error) {
	if !repo.armed {
		return repo.opsLogFilterSettingsStub.GetValue(ctx, key)
	}
	<-ctx.Done()
	return "", ctx.Err()
}

func TestOpsErrorLogger_DetachedSettingsReadsAreBounded(test *testing.T) {
	gin.SetMode(gin.TestMode)
	prepareOpsLogFilterTestQueue(test)
	repo := &opsBlockingSettingsStub{opsLogFilterSettingsStub: opsLogFilterSettingsStub{values: map[string]string{}}}
	ops := service.NewOpsService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	repo.armed = true
	// The client hangs up, so only the detached settings reads could block.
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(ctx *gin.Context) {
		cancel()
		ctx.JSON(http.StatusBadGateway, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream request failed"}})
	})
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestCtx))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * opsSettingsReadTimeout):
		test.Fatal("ops settings reads were not bounded")
	}
}
