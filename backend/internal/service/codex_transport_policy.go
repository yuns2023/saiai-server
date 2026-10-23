package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// codexTransportPolicyFor selects the pinned Codex TLS transport identity for
// a request made on behalf of the official Codex client. It returns the zero
// policy (standard Go TLS) unless the global gateway.tls_fingerprint.enabled
// switch is on AND the OpenAI OAuth account explicitly opted in. The role is
// chosen by the caller from the request purpose; it is never inferred from
// the client's User-Agent.
func codexTransportPolicyFor(cfg *config.Config, account *Account, role tlsfingerprint.Role) tlsfingerprint.TransportPolicy {
	if cfg == nil || !cfg.Gateway.TLSFingerprint.Enabled || !account.IsCodexTLSFingerprintEnabled() {
		return tlsfingerprint.TransportPolicy{}
	}
	switch role {
	case tlsfingerprint.RoleHTTP:
		return tlsfingerprint.CodexHTTPTransportPolicy()
	case tlsfingerprint.RoleWS:
		return tlsfingerprint.CodexWSTransportPolicy()
	default:
		return tlsfingerprint.TransportPolicy{}
	}
}

// codexControlTransport lets services that historically used the shared
// httpclient pool send Codex control-plane requests with the account's Codex
// TLS identity. Unset or opted-out accounts keep the legacy client.
type codexControlTransport struct {
	cfg      *config.Config
	upstream HTTPUpstream
}

// policyFor returns the Codex HTTP policy for account (zero when disabled).
func (t codexControlTransport) policyFor(account *Account) tlsfingerprint.TransportPolicy {
	return codexTransportPolicyFor(t.cfg, account, tlsfingerprint.RoleHTTP)
}

// do sends req with policy through the account's upstream pool. A requested
// policy without an upstream fails instead of using a different TLS stack.
func (t codexControlTransport) do(req *http.Request, proxyURL string, account *Account, policy tlsfingerprint.TransportPolicy) (*http.Response, error) {
	if t.upstream == nil {
		return nil, ErrTransportPolicyUnsupported
	}
	return doWithTransportPolicy(t.upstream, req, proxyURL, account.ID, account.Concurrency, policy)
}

// currentCodexWSTransportPolicy resolves the WS policy from the account's
// current state (scheduler snapshot cache, then repository) so a toggle
// reaches new upstream dials of long-lived inbound sessions and prewarm, whose
// *Account may be a snapshot taken when the session started. If the lookup
// fails, the caller's snapshot decides.
func (s *OpenAIGatewayService) currentCodexWSTransportPolicy(ctx context.Context, account *Account) tlsfingerprint.TransportPolicy {
	if s == nil || account == nil {
		return tlsfingerprint.TransportPolicy{}
	}
	current := account
	var (
		fresh *Account
		err   error
	)
	if s.schedulerSnapshot != nil {
		fresh, err = s.schedulerSnapshot.GetAccount(ctx, account.ID)
	} else if s.accountRepo != nil {
		fresh, err = s.accountRepo.GetByID(ctx, account.ID)
	}
	if err == nil && fresh != nil && fresh.ID == account.ID {
		current = fresh
	}
	return codexTransportPolicyFor(s.cfg, current, tlsfingerprint.RoleWS)
}
