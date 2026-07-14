package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type claudeCredentialsFile struct {
	ClaudeAiOauth *claudeAiOauthCredentials `json:"claudeAiOauth"`
}

type claudeAiOauthCredentials struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

type cliOptions struct {
	File                 string
	Name                 string
	Notes                string
	AccountUUID          string
	FixedDeviceID        string
	FixedHeadersFile     string
	OrgUUID              string
	Email                string
	ProxyURL             string
	ProxyName            string
	ServerURL            string
	AdminToken           string
	AdminEmail           string
	AdminPassword        string
	ProxyID              int64
	GroupIDs             []int64
	Mode                 string
	CarpoolDeviceLimit   int
	SharedBucketCount    int
	Concurrency          int
	Priority             int
	SkipDefaultGroupBind bool
	DryRun               bool
}

type importSummary struct {
	ID                       int64    `json:"id,omitempty"`
	Name                     string   `json:"name"`
	Platform                 string   `json:"platform"`
	Type                     string   `json:"type"`
	GroupIDs                 []int64  `json:"group_ids,omitempty"`
	ProxyID                  *int64   `json:"proxy_id,omitempty"`
	ProxyURL                 string   `json:"proxy_url,omitempty"`
	ProxyName                string   `json:"proxy_name,omitempty"`
	ClaudeOAuthMode          string   `json:"claude_oauth_mode"`
	ClaudeOAuthCarpoolLimit  *int     `json:"claude_oauth_carpool_device_limit,omitempty"`
	ClaudeOAuthSharedBuckets *int     `json:"claude_oauth_shared_bucket_count,omitempty"`
	FixedDeviceID            string   `json:"claude_oauth_fixed_device_id,omitempty"`
	FixedHeadersText         string   `json:"claude_oauth_fixed_headers_text,omitempty"`
	AccountUUID              string   `json:"account_uuid"`
	OrgUUID                  string   `json:"org_uuid,omitempty"`
	EmailAddress             string   `json:"email_address,omitempty"`
	ExpiresAt                int64    `json:"expires_at"`
	Scope                    string   `json:"scope,omitempty"`
	CredentialKeys           []string `json:"credential_keys"`
	SubscriptionType         string   `json:"subscription_type,omitempty"`
	RateLimitTier            string   `json:"rate_limit_tier,omitempty"`
	DryRun                   bool     `json:"dry_run"`
}

type importedProxySpec struct {
	Name     string
	RawURL   string
	Redacted string
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
	Parsed   *url.URL
}

type claudeOAuthProfileResponse struct {
	Account      *claudeOAuthProfileAccount      `json:"account"`
	Organization *claudeOAuthProfileOrganization `json:"organization"`
}

type claudeOAuthProfileAccount struct {
	UUID  string `json:"uuid"`
	Email string `json:"email"`
}

type claudeOAuthProfileOrganization struct {
	UUID             string `json:"uuid"`
	OrganizationType string `json:"organization_type"`
	RateLimitTier    string `json:"rate_limit_tier"`
}

const claudeOAuthProfileURL = "https://api.anthropic.com/api/oauth/profile"

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		exitErr(err)
	}

	fileData, err := loadClaudeCredentialsFile(opts.File)
	if err != nil {
		exitErr(err)
	}

	proxySpec, err := parseImportedProxy(opts.ProxyURL, opts.ProxyName)
	if err != nil {
		exitErr(err)
	}

	resolvedFileData := *fileData
	resolvedOpts := *opts

	detectCtx, detectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	profile, err := detectClaudeOAuthProfile(detectCtx, strings.TrimSpace(fileData.AccessToken), proxySpec)
	detectCancel()
	if err != nil {
		exitErr(fmt.Errorf("detect claude oauth profile: %w", err))
	}
	applyDetectedProfile(&resolvedFileData, &resolvedOpts, profile)

	if resolvedOpts.DryRun {
		_, summary, err := buildCreateAccountInput(&resolvedFileData, &resolvedOpts)
		if err != nil {
			exitErr(err)
		}
		summary.ProxyURL = proxySpec.Redacted
		summary.ProxyName = proxySpec.Name
		printSummary(summary)
		return
	}

	finalSummary, err := importViaAPI(&resolvedFileData, &resolvedOpts, proxySpec)
	if err != nil {
		exitErr(err)
	}
	printSummary(finalSummary)
}

func parseFlags(args []string) (*cliOptions, error) {
	defaultPath := "~/.claude/.credentials.json"
	fs := flag.NewFlagSet("claudeoauthimport", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	file := fs.String("file", defaultPath, "Path to Claude Code .credentials.json")
	name := fs.String("name", "", "Account name (defaults to claude-oauth-import)")
	notes := fs.String("notes", "", "Optional account notes")
	accountUUID := fs.String("account-uuid", "", "Optional Anthropic account UUID override (auto-detected by default)")
	fixedDeviceID := fs.String("fixed-device-id", "", "Fixed Claude metadata.user_id.device_id for single_device mode")
	fixedHeadersFile := fs.String("fixed-headers-file", "", "Optional text file containing fixed x-stainless-* headers for single_device mode")
	orgUUID := fs.String("org-uuid", "", "Optional organization UUID override")
	email := fs.String("email", "", "Optional email override")
	proxyURL := fs.String("proxy-url", "", "Required proxy URL used for profile detection and imported into the backend system")
	proxyName := fs.String("proxy-name", "", "Optional proxy name override")
	serverURL := fs.String("server-url", strings.TrimSpace(os.Getenv("SUB2API_SERVER_URL")), "Optional Sub2API base URL; when set, import goes through the admin HTTP API instead of direct database access")
	adminToken := fs.String("admin-token", strings.TrimSpace(os.Getenv("SUB2API_ADMIN_TOKEN")), "Optional admin bearer token for API mode")
	adminEmail := fs.String("admin-email", strings.TrimSpace(os.Getenv("SUB2API_ADMIN_EMAIL")), "Optional admin email for API login in API mode")
	adminPassword := fs.String("admin-password", strings.TrimSpace(os.Getenv("SUB2API_ADMIN_PASSWORD")), "Optional admin password for API login in API mode")
	groupIDs := fs.String("group-ids", "", "Optional comma-separated group IDs")
	mode := fs.String("mode", "carpool", "Claude OAuth mode: carpool, shared, pinned, or single_device")
	carpoolLimit := fs.Int("carpool-device-limit", 5, "Carpool mode device limit")
	sharedBuckets := fs.Int("shared-bucket-count", 5, "Shared mode bucket count")
	concurrency := fs.Int("concurrency", 3, "Account concurrency")
	priority := fs.Int("priority", 0, "Account priority")
	skipDefaultGroupBind := fs.Bool("skip-default-group-bind", false, "Skip auto-binding to platform default group")
	dryRun := fs.Bool("dry-run", false, "Validate and print the import payload without creating the account")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	parsedGroupIDs, err := parseGroupIDs(*groupIDs)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(*accountUUID) != "" {
		if _, err := uuid.Parse(strings.TrimSpace(*accountUUID)); err != nil {
			return nil, fmt.Errorf("invalid --account-uuid: %w", err)
		}
	}
	if strings.TrimSpace(*proxyURL) == "" {
		return nil, errors.New("--proxy-url is required because claudeoauthimport performs a network profile lookup and must not connect directly")
	}
	if _, err := parseImportedProxy(*proxyURL, *proxyName); err != nil {
		return nil, fmt.Errorf("invalid --proxy-url: %w", err)
	}
	if !*dryRun && strings.TrimSpace(*serverURL) == "" {
		return nil, errors.New("--server-url is required for non-dry-run imports; claudeoauthimport now writes accounts through the admin HTTP API instead of direct database access")
	}
	if !*dryRun && strings.TrimSpace(*adminToken) == "" && (strings.TrimSpace(*adminEmail) == "" || strings.TrimSpace(*adminPassword) == "") {
		return nil, errors.New("non-dry-run API imports require --admin-token or both --admin-email and --admin-password")
	}
	modeValue := strings.TrimSpace(strings.ToLower(*mode))
	if modeValue != "carpool" && modeValue != "shared" && modeValue != "pinned" && modeValue != "single_device" {
		return nil, errors.New("--mode must be carpool, shared, pinned, or single_device")
	}
	if *concurrency <= 0 {
		return nil, errors.New("--concurrency must be > 0")
	}
	if *carpoolLimit <= 0 || *carpoolLimit > 32 {
		return nil, errors.New("--carpool-device-limit must be between 1 and 32")
	}
	if *sharedBuckets <= 0 || *sharedBuckets > 32 {
		return nil, errors.New("--shared-bucket-count must be between 1 and 32")
	}

	return &cliOptions{
		File:                 *file,
		Name:                 strings.TrimSpace(*name),
		Notes:                strings.TrimSpace(*notes),
		AccountUUID:          strings.TrimSpace(*accountUUID),
		FixedDeviceID:        strings.TrimSpace(*fixedDeviceID),
		FixedHeadersFile:     strings.TrimSpace(*fixedHeadersFile),
		OrgUUID:              strings.TrimSpace(*orgUUID),
		Email:                strings.TrimSpace(*email),
		ProxyURL:             strings.TrimSpace(*proxyURL),
		ProxyName:            strings.TrimSpace(*proxyName),
		ServerURL:            strings.TrimSpace(*serverURL),
		AdminToken:           strings.TrimSpace(*adminToken),
		AdminEmail:           strings.TrimSpace(*adminEmail),
		AdminPassword:        strings.TrimSpace(*adminPassword),
		GroupIDs:             parsedGroupIDs,
		Mode:                 modeValue,
		CarpoolDeviceLimit:   *carpoolLimit,
		SharedBucketCount:    *sharedBuckets,
		Concurrency:          *concurrency,
		Priority:             *priority,
		SkipDefaultGroupBind: *skipDefaultGroupBind,
		DryRun:               *dryRun,
	}, nil
}

func parseGroupIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid group id %q", part)
		}
		ids = append(ids, v)
	}
	return ids, nil
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func loadClaudeCredentialsFile(path string) (*claudeAiOauthCredentials, error) {
	fullPath := expandPath(path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}
	var parsed claudeCredentialsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse credentials file: %w", err)
	}
	if parsed.ClaudeAiOauth == nil {
		return nil, errors.New(`missing "claudeAiOauth" object in credentials file`)
	}
	if strings.TrimSpace(parsed.ClaudeAiOauth.AccessToken) == "" {
		return nil, errors.New("credentials file is missing claudeAiOauth.accessToken")
	}
	if strings.TrimSpace(parsed.ClaudeAiOauth.RefreshToken) == "" {
		return nil, errors.New("credentials file is missing claudeAiOauth.refreshToken")
	}
	if parsed.ClaudeAiOauth.ExpiresAt <= 0 {
		return nil, errors.New("credentials file is missing a valid claudeAiOauth.expiresAt")
	}
	return parsed.ClaudeAiOauth, nil
}

func normalizeExpiresAt(raw int64) int64 {
	if raw > 1_000_000_000_000 {
		return raw / 1000
	}
	return raw
}

func loadOptionalTextFile(path string) (string, error) {
	fullPath := expandPath(path)
	if strings.TrimSpace(fullPath) == "" {
		return "", nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func buildCreateAccountInput(fileData *claudeAiOauthCredentials, opts *cliOptions) (*service.CreateAccountInput, *importSummary, error) {
	if fileData == nil {
		return nil, nil, errors.New("nil claude credentials")
	}
	if strings.TrimSpace(opts.AccountUUID) == "" {
		return nil, nil, errors.New("missing account_uuid; provide --account-uuid or allow profile detection to populate it")
	}
	fixedHeadersText, err := loadOptionalTextFile(opts.FixedHeadersFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read fixed headers file: %w", err)
	}
	if strings.TrimSpace(fixedHeadersText) != "" {
		if err := service.ValidateClaudeOAuthSingleDeviceFixedHeaders(fixedHeadersText); err != nil {
			return nil, nil, fmt.Errorf("invalid fixed headers: %w", err)
		}
	}
	if opts.Mode == service.ClaudeOAuthModeSingleDevice && strings.TrimSpace(opts.FixedDeviceID) == "" {
		return nil, nil, errors.New("single_device mode requires --fixed-device-id")
	}

	expiresAt := normalizeExpiresAt(fileData.ExpiresAt)
	credentials := map[string]any{
		"access_token":  strings.TrimSpace(fileData.AccessToken),
		"refresh_token": strings.TrimSpace(fileData.RefreshToken),
		"token_type":    "Bearer",
		"expires_at":    expiresAt,
	}
	if delta := expiresAt - time.Now().Unix(); delta > 0 {
		credentials["expires_in"] = delta
	}
	scope := strings.TrimSpace(strings.Join(fileData.Scopes, " "))
	if scope != "" {
		credentials["scope"] = scope
	}

	extra := map[string]any{
		"account_uuid":      opts.AccountUUID,
		"claude_oauth_mode": opts.Mode,
	}
	if opts.OrgUUID != "" {
		extra["org_uuid"] = opts.OrgUUID
		credentials["org_uuid"] = opts.OrgUUID
	}
	if opts.Email != "" {
		extra["email_address"] = opts.Email
		credentials["email_address"] = opts.Email
	}
	if fileData.SubscriptionType != "" {
		extra["subscription_type"] = strings.TrimSpace(fileData.SubscriptionType)
	}
	if fileData.RateLimitTier != "" {
		extra["rate_limit_tier"] = strings.TrimSpace(fileData.RateLimitTier)
	}
	switch opts.Mode {
	case "shared":
		extra["claude_oauth_shared_bucket_count"] = opts.SharedBucketCount
	case "carpool":
		extra["claude_oauth_carpool_device_limit"] = opts.CarpoolDeviceLimit
	case service.ClaudeOAuthModeSingleDevice:
		extra["claude_oauth_fixed_device_id"] = strings.TrimSpace(opts.FixedDeviceID)
		if fixedHeadersText != "" {
			extra["claude_oauth_fixed_headers_text"] = fixedHeadersText
		}
	}

	name := opts.Name
	if name == "" {
		name = defaultImportedAccountName(fileData, opts)
	}
	var notes *string
	if opts.Notes != "" {
		notes = &opts.Notes
	}
	var proxyID *int64
	if opts.ProxyID > 0 {
		proxyID = &opts.ProxyID
	}

	input := &service.CreateAccountInput{
		Name:                 name,
		Notes:                notes,
		Platform:             service.PlatformAnthropic,
		Type:                 service.AccountTypeOAuth,
		Credentials:          credentials,
		Extra:                extra,
		ProxyID:              proxyID,
		Concurrency:          opts.Concurrency,
		Priority:             opts.Priority,
		GroupIDs:             opts.GroupIDs,
		SkipDefaultGroupBind: opts.SkipDefaultGroupBind,
	}

	summary := &importSummary{
		Name:             name,
		Platform:         input.Platform,
		Type:             input.Type,
		GroupIDs:         append([]int64(nil), opts.GroupIDs...),
		ProxyID:          proxyID,
		ProxyURL:         opts.ProxyURL,
		ProxyName:        opts.ProxyName,
		ClaudeOAuthMode:  opts.Mode,
		FixedDeviceID:    strings.TrimSpace(opts.FixedDeviceID),
		FixedHeadersText: fixedHeadersText,
		AccountUUID:      opts.AccountUUID,
		OrgUUID:          opts.OrgUUID,
		EmailAddress:     opts.Email,
		ExpiresAt:        expiresAt,
		Scope:            scope,
		CredentialKeys:   sortedKeys(credentials),
		SubscriptionType: strings.TrimSpace(fileData.SubscriptionType),
		RateLimitTier:    strings.TrimSpace(fileData.RateLimitTier),
		DryRun:           opts.DryRun,
	}
	switch opts.Mode {
	case "shared":
		v := opts.SharedBucketCount
		summary.ClaudeOAuthSharedBuckets = &v
	case "carpool":
		v := opts.CarpoolDeviceLimit
		summary.ClaudeOAuthCarpoolLimit = &v
	}

	return input, summary, nil
}

func defaultImportedAccountName(fileData *claudeAiOauthCredentials, opts *cliOptions) string {
	if opts.Email != "" {
		return opts.Email
	}
	acc := opts.AccountUUID
	if len(acc) > 8 {
		acc = acc[:8]
	}
	sub := strings.TrimSpace(fileData.SubscriptionType)
	if sub == "" {
		sub = "unknown"
	}
	return fmt.Sprintf("claude-oauth-%s-%s", sub, acc)
}

func sortedKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(items []string) {
	if len(items) < 2 {
		return
	}
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j] < items[i] {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

func parseImportedProxy(rawURL, overrideName string) (*importedProxySpec, error) {
	trimmed, parsed, err := proxyurl.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("proxy URL is required")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("proxy URL must include a valid port")
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = defaultImportedProxyName(parsed.Scheme, parsed.Hostname(), port)
	}
	return &importedProxySpec{
		Name:     name,
		RawURL:   trimmed,
		Redacted: parsed.Redacted(),
		Protocol: parsed.Scheme,
		Host:     parsed.Hostname(),
		Port:     port,
		Username: username,
		Password: password,
		Parsed:   parsed,
	}, nil
}

func defaultImportedProxyName(protocol, host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "proxy"
	}
	host = strings.ReplaceAll(host, ":", "_")
	return fmt.Sprintf("claudeoauthimport-%s-%s-%d", strings.ToLower(strings.TrimSpace(protocol)), host, port)
}

func detectClaudeOAuthProfile(ctx context.Context, accessToken string, proxySpec *importedProxySpec) (*claudeOAuthProfileResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("missing access token")
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || baseTransport == nil {
		return nil, errors.New("default transport is not *http.Transport")
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	if err := proxyutil.ConfigureTransportProxy(transport, proxySpec.Parsed); err != nil {
		return nil, fmt.Errorf("configure proxy transport: %w", err)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeOAuthProfileURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return nil, fmt.Errorf("profile lookup failed: status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var profile claudeOAuthProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decode profile response: %w", err)
	}
	if profile.Account == nil || strings.TrimSpace(profile.Account.UUID) == "" {
		return nil, errors.New("profile response missing account.uuid")
	}
	return &profile, nil
}

func applyDetectedProfile(fileData *claudeAiOauthCredentials, opts *cliOptions, profile *claudeOAuthProfileResponse) {
	if fileData == nil || opts == nil || profile == nil {
		return
	}
	if opts.AccountUUID == "" && profile.Account != nil {
		opts.AccountUUID = strings.TrimSpace(profile.Account.UUID)
	}
	if opts.Email == "" && profile.Account != nil {
		opts.Email = strings.TrimSpace(profile.Account.Email)
	}
	if opts.OrgUUID == "" && profile.Organization != nil {
		opts.OrgUUID = strings.TrimSpace(profile.Organization.UUID)
	}
	if strings.TrimSpace(fileData.SubscriptionType) == "" && profile.Organization != nil {
		switch strings.TrimSpace(strings.ToLower(profile.Organization.OrganizationType)) {
		case "claude_max":
			fileData.SubscriptionType = "max"
		case "claude_pro":
			fileData.SubscriptionType = "pro"
		case "claude_enterprise":
			fileData.SubscriptionType = "enterprise"
		case "claude_team":
			fileData.SubscriptionType = "team"
		}
	}
	if strings.TrimSpace(fileData.RateLimitTier) == "" && profile.Organization != nil {
		fileData.RateLimitTier = strings.TrimSpace(profile.Organization.RateLimitTier)
	}
}

type apiEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type apiLoginResponse struct {
	AccessToken string `json:"access_token"`
}

type apiProxyList struct {
	Items []apiProxy `json:"items"`
	Page  int        `json:"page"`
	Pages int        `json:"pages"`
	Total int64      `json:"total"`
}

type apiProxy struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type apiAccount struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
	ProxyID     *int64         `json:"proxy_id"`
	GroupIDs    []int64        `json:"group_ids"`
}

func importViaAPI(fileData *claudeAiOauthCredentials, opts *cliOptions, proxySpec *importedProxySpec) (*importSummary, error) {
	serverURL := strings.TrimRight(strings.TrimSpace(opts.ServerURL), "/")
	if serverURL == "" {
		return nil, errors.New("missing --server-url for API mode")
	}
	token, err := resolveAdminToken(serverURL, opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	proxyID, err := ensureImportedProxyViaAPI(ctx, serverURL, token, proxySpec)
	if err != nil {
		return nil, fmt.Errorf("ensure proxy via api: %w", err)
	}
	opts.ProxyID = proxyID

	input, summary, err := buildCreateAccountInput(fileData, opts)
	if err != nil {
		return nil, err
	}
	summary.ProxyURL = proxySpec.Redacted
	summary.ProxyName = proxySpec.Name

	created, err := createAccountViaAPI(ctx, serverURL, token, input)
	if err != nil {
		return nil, fmt.Errorf("create account via api: %w", err)
	}
	finalSummary := summaryFromAPIAccount(created, fileData)
	finalSummary.ProxyURL = proxySpec.Redacted
	finalSummary.ProxyName = proxySpec.Name
	return finalSummary, nil
}

func resolveAdminToken(serverURL string, opts *cliOptions) (string, error) {
	if strings.TrimSpace(opts.AdminToken) != "" {
		return strings.TrimSpace(opts.AdminToken), nil
	}
	if strings.TrimSpace(opts.AdminEmail) == "" || strings.TrimSpace(opts.AdminPassword) == "" {
		return "", errors.New("API mode requires --admin-token or both --admin-email and --admin-password")
	}
	reqBody := map[string]any{
		"email":           strings.TrimSpace(opts.AdminEmail),
		"password":        opts.AdminPassword,
		"turnstile_token": "",
	}
	var env apiEnvelope[apiLoginResponse]
	if err := doJSONRequest(context.Background(), http.MethodPost, serverURL+"/api/v1/auth/login", "", reqBody, &env); err != nil {
		return "", fmt.Errorf("admin login failed: %w", err)
	}
	if strings.TrimSpace(env.Data.AccessToken) == "" {
		return "", errors.New("admin login returned empty access_token")
	}
	return strings.TrimSpace(env.Data.AccessToken), nil
}

func ensureImportedProxyViaAPI(ctx context.Context, serverURL, token string, proxySpec *importedProxySpec) (int64, error) {
	existing, err := findExistingImportedProxyViaAPI(ctx, serverURL, token, proxySpec)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		if existing.Status != service.StatusActive {
			updateBody := map[string]any{"status": service.StatusActive}
			var env apiEnvelope[apiProxy]
			if err := doJSONRequest(ctx, http.MethodPut, fmt.Sprintf("%s/api/v1/admin/proxies/%d", serverURL, existing.ID), token, updateBody, &env); err != nil {
				return 0, fmt.Errorf("reactivate proxy %d: %w", existing.ID, err)
			}
		}
		return existing.ID, nil
	}
	createBody := map[string]any{
		"name":     proxySpec.Name,
		"protocol": proxySpec.Protocol,
		"host":     proxySpec.Host,
		"port":     proxySpec.Port,
		"username": proxySpec.Username,
		"password": proxySpec.Password,
	}
	var env apiEnvelope[apiProxy]
	if err := doJSONRequest(ctx, http.MethodPost, serverURL+"/api/v1/admin/proxies", token, createBody, &env); err != nil {
		return 0, err
	}
	return env.Data.ID, nil
}

func findExistingImportedProxyViaAPI(ctx context.Context, serverURL, token string, proxySpec *importedProxySpec) (*apiProxy, error) {
	page := 1
	for {
		var env apiEnvelope[apiProxyList]
		endpoint := fmt.Sprintf("%s/api/v1/admin/proxies?page=%d&page_size=100", serverURL, page)
		if err := doJSONRequest(ctx, http.MethodGet, endpoint, token, nil, &env); err != nil {
			return nil, err
		}
		for i := range env.Data.Items {
			item := env.Data.Items[i]
			if item.Protocol == proxySpec.Protocol &&
				item.Host == proxySpec.Host &&
				item.Port == proxySpec.Port &&
				item.Username == proxySpec.Username &&
				item.Password == proxySpec.Password {
				return &item, nil
			}
		}
		if env.Data.Pages <= 0 || page >= env.Data.Pages {
			break
		}
		page++
	}
	return nil, nil
}

func createAccountViaAPI(ctx context.Context, serverURL, token string, input *service.CreateAccountInput) (*apiAccount, error) {
	body := map[string]any{
		"name":                    input.Name,
		"notes":                   input.Notes,
		"platform":                input.Platform,
		"type":                    input.Type,
		"credentials":             input.Credentials,
		"extra":                   input.Extra,
		"proxy_id":                input.ProxyID,
		"concurrency":             input.Concurrency,
		"priority":                input.Priority,
		"group_ids":               input.GroupIDs,
		"skip_default_group_bind": input.SkipDefaultGroupBind,
	}
	if input.RateMultiplier != nil {
		body["rate_multiplier"] = *input.RateMultiplier
	}
	if input.LoadFactor != nil {
		body["load_factor"] = *input.LoadFactor
	}
	if input.ExpiresAt != nil {
		body["expires_at"] = *input.ExpiresAt
	}
	if input.AutoPauseOnExpired != nil {
		body["auto_pause_on_expired"] = *input.AutoPauseOnExpired
	}
	var env apiEnvelope[apiAccount]
	if err := doJSONRequest(ctx, http.MethodPost, serverURL+"/api/v1/admin/accounts", token, body, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

func summaryFromAPIAccount(account *apiAccount, fileData *claudeAiOauthCredentials) *importSummary {
	if account == nil {
		return nil
	}
	summary := &importSummary{
		ID:               account.ID,
		Name:             account.Name,
		Platform:         account.Platform,
		Type:             account.Type,
		GroupIDs:         append([]int64(nil), account.GroupIDs...),
		ProxyID:          account.ProxyID,
		ClaudeOAuthMode:  getMapString(account.Extra, "claude_oauth_mode"),
		FixedDeviceID:    getMapString(account.Extra, "claude_oauth_fixed_device_id"),
		FixedHeadersText: getMapString(account.Extra, "claude_oauth_fixed_headers_text"),
		AccountUUID:      getMapString(account.Extra, "account_uuid"),
		OrgUUID:          getMapString(account.Extra, "org_uuid"),
		EmailAddress:     getMapString(account.Extra, "email_address"),
		CredentialKeys:   sortedKeys(account.Credentials),
		DryRun:           false,
	}
	if expiresAt := getMapInt64(account.Credentials, "expires_at"); expiresAt > 0 {
		summary.ExpiresAt = expiresAt
	}
	summary.Scope = getMapString(account.Credentials, "scope")
	summary.SubscriptionType = getMapString(account.Extra, "subscription_type")
	summary.RateLimitTier = getMapString(account.Extra, "rate_limit_tier")
	switch summary.ClaudeOAuthMode {
	case "shared":
		if v := int(getMapInt64(account.Extra, "claude_oauth_shared_bucket_count")); v > 0 {
			summary.ClaudeOAuthSharedBuckets = &v
		}
	case "carpool":
		if v := int(getMapInt64(account.Extra, "claude_oauth_carpool_device_limit")); v > 0 {
			summary.ClaudeOAuthCarpoolLimit = &v
		}
	}
	if summary.SubscriptionType == "" && fileData != nil {
		summary.SubscriptionType = strings.TrimSpace(fileData.SubscriptionType)
	}
	if summary.RateLimitTier == "" && fileData != nil {
		summary.RateLimitTier = strings.TrimSpace(fileData.RateLimitTier)
	}
	return summary
}

func doJSONRequest(ctx context.Context, method, endpoint, bearer string, reqBody any, out any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return fmt.Errorf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func getMapString(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	if v, ok := m[key]; ok {
		switch vv := v.(type) {
		case string:
			return strings.TrimSpace(vv)
		}
	}
	return ""
}

func getMapInt64(m map[string]any, key string) int64 {
	if len(m) == 0 {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch vv := v.(type) {
	case int64:
		return vv
	case int:
		return int64(vv)
	case float64:
		return int64(vv)
	case json.Number:
		n, _ := vv.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(vv), 10, 64)
		return n
	default:
		return 0
	}
}

func printSummary(summary *importSummary) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summary); err != nil {
		exitErr(err)
	}
}

func exitErr(err error) {
	fmt.Fprintf(os.Stderr, "claudeoauthimport: %v\n", err)
	os.Exit(1)
}
