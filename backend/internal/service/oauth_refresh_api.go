package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// OAuthRefreshExecutor 各平台实现的 OAuth 刷新执行器
// TokenRefresher 接口的超集：增加了 CacheKey 方法用于分布式锁
type OAuthRefreshExecutor interface {
	TokenRefresher

	// CacheKey 返回用于分布式锁的缓存键（与 TokenProvider 使用的一致）
	CacheKey(account *Account) string
}

const refreshLockTTL = 30 * time.Second

// OAuthRefreshResult 统一刷新结果
type OAuthRefreshResult struct {
	Refreshed      bool           // 实际执行了刷新
	NewCredentials map[string]any // 刷新后的 credentials（nil 表示未刷新）
	Account        *Account       // 从 DB 重新读取的最新 account
	LockHeld       bool           // 锁被其他 worker 持有（未执行刷新）
	// RecoveryBasis is the account as re-read under the lock, before the
	// upstream call. Post-refresh recovery (clearing a 401 cooldown or a
	// missing_project_id error) decides from it, not from Account: state set
	// by other requests during the upstream call is not this refresh's to
	// clear.
	RecoveryBasis *Account
}

// OAuthRefreshAPI 统一的 OAuth Token 刷新入口
// 封装分布式锁、DB 重读、已刷新检查等通用逻辑
type OAuthRefreshAPI struct {
	accountRepo AccountRepository
	tokenCache  GeminiTokenCache // 可选，nil = 无锁
}

// NewOAuthRefreshAPI 创建统一刷新 API
func NewOAuthRefreshAPI(accountRepo AccountRepository, tokenCache GeminiTokenCache) *OAuthRefreshAPI {
	return &OAuthRefreshAPI{
		accountRepo: accountRepo,
		tokenCache:  tokenCache,
	}
}

// RefreshIfNeeded 在分布式锁保护下按需刷新 OAuth token
//
// 流程:
//  1. 获取分布式锁
//  2. 从 DB 重读最新 account（防止使用过时的 refresh_token）
//  3. 二次检查是否仍需刷新
//  4. 调用 executor.Refresh() 执行平台特定刷新逻辑
//  5. 设置 _token_version + 更新 DB
//  6. 释放锁
func (api *OAuthRefreshAPI) RefreshIfNeeded(
	ctx context.Context,
	account *Account,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
) (*OAuthRefreshResult, error) {
	cacheKey := executor.CacheKey(account)

	// 1. 获取分布式锁
	lockAcquired := false
	if api.tokenCache != nil {
		acquired, lockErr := api.tokenCache.AcquireRefreshLock(ctx, cacheKey, refreshLockTTL)
		if lockErr != nil {
			// Redis 错误，降级为无锁刷新
			slog.Warn("oauth_refresh_lock_failed_degraded",
				"account_id", account.ID,
				"cache_key", cacheKey,
				"error", lockErr,
			)
		} else if !acquired {
			// 锁被其他 worker 持有
			return &OAuthRefreshResult{LockHeld: true}, nil
		} else {
			lockAcquired = true
			defer func() { _ = api.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
		}
	}

	// 2. 从 DB 重读最新 account（锁保护下，确保使用最新的 refresh_token）
	freshAccount, err := api.accountRepo.GetByID(ctx, account.ID)
	if errors.Is(err, ErrAccountNotFound) {
		// Deleted meanwhile: refreshing would rotate the upstream token of an
		// account that no longer exists.
		return nil, err
	}
	if err != nil {
		slog.Warn("oauth_refresh_db_reread_failed",
			"account_id", account.ID,
			"error", err,
		)
		// 降级使用传入的 account
		freshAccount = account
	} else if freshAccount == nil {
		freshAccount = account
	}

	// 3. 二次检查是否仍需刷新（另一条路径可能已刷新）
	if !executor.NeedsRefresh(freshAccount, refreshWindow) {
		return &OAuthRefreshResult{
			Account: freshAccount,
		}, nil
	}

	recoveryBasis := *freshAccount

	// 4. 执行平台特定刷新逻辑
	newCredentials, refreshErr := executor.Refresh(ctx, freshAccount)
	if refreshErr != nil {
		return nil, refreshErr
	}

	// 5. 设置版本号 + 更新 DB
	// Only the credentials column is written: the upstream call above takes
	// seconds, and a 429 mark, disable or usage sample written meanwhile must
	// survive. The returned Account is re-read after the write.
	if newCredentials != nil {
		newCredentials["_token_version"] = time.Now().UnixMilli()
		if updateErr := api.accountRepo.UpdateCredentials(ctx, freshAccount.ID, newCredentials); updateErr != nil {
			slog.Error("oauth_refresh_update_failed",
				"account_id", freshAccount.ID,
				"error", updateErr,
			)
			return nil, fmt.Errorf("oauth refresh succeeded but DB update failed: %w", updateErr)
		}
		freshAccount = rereadAfterCredentialsWrite(ctx, api.accountRepo, freshAccount, newCredentials)
	}

	_ = lockAcquired // suppress unused warning when tokenCache is nil

	return &OAuthRefreshResult{
		Refreshed:      true,
		NewCredentials: newCredentials,
		Account:        freshAccount,
		RecoveryBasis:  &recoveryBasis,
	}, nil
}

// rereadAfterCredentialsWrite returns the committed account after a
// credentials-only write; if the re-read fails it falls back to the object the
// refresh started from, carrying the new credentials.
func rereadAfterCredentialsWrite(ctx context.Context, repo AccountRepository, account *Account, credentials map[string]any) *Account {
	if fresh, err := repo.GetByID(ctx, account.ID); err == nil && fresh != nil {
		return fresh
	}
	fallback := *account
	fallback.Credentials = credentials
	return &fallback
}

// adoptStoredCredentials copies keys from the stored credentials into the
// in-memory account after a guarded backfill lost to a concurrent write, so
// the request does not use the rejected value.
func adoptStoredCredentials(ctx context.Context, repo AccountRepository, account *Account, keys ...string) {
	stored, err := repo.GetByID(ctx, account.ID)
	if err != nil || stored == nil {
		// Keeps whatever the request already had (never the rejected value).
		slog.Warn("credentials_backfill_reread_failed", "account_id", account.ID, "error", err)
		return
	}
	for _, k := range keys {
		if v, ok := stored.Credentials[k]; ok {
			account.Credentials[k] = v
		} else {
			delete(account.Credentials, k)
		}
	}
}

// MergeCredentials 将旧 credentials 中不存在于新 map 的字段保留到新 map 中
func MergeCredentials(oldCreds, newCreds map[string]any) map[string]any {
	if newCreds == nil {
		newCreds = make(map[string]any)
	}
	for k, v := range oldCreds {
		if _, exists := newCreds[k]; !exists {
			newCreds[k] = v
		}
	}
	return newCreds
}

// BuildClaudeAccountCredentials 为 Claude 平台构建 OAuth credentials map
// 消除 Claude 平台没有 BuildAccountCredentials 方法的问题
func BuildClaudeAccountCredentials(tokenInfo *TokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"token_type":   tokenInfo.TokenType,
		"expires_in":   strconv.FormatInt(tokenInfo.ExpiresIn, 10),
		"expires_at":   strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	return creds
}
