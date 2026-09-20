package service

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID           int64
	Email        string
	Username     string
	Notes        string
	PasswordHash string
	Role         string
	Balance      float64
	// PaygDiscountMultiplier discounts this user's balance billing across keys,
	// groups, and upstream accounts. Nil in older auth cache entries means 1.
	PaygDiscountMultiplier      *float64
	PaygDiscountOverrideEnabled bool
	AutoLevelID                 *int64
	ManualLevelID               *int64
	AutoLevel                   *AccessLevel
	ManualLevel                 *AccessLevel
	Concurrency                 int
	Status                      string
	AllowedGroups               []int64
	TokenVersion                int64 // Incremented on password change to invalidate existing tokens
	CreatedAt                   time.Time
	UpdatedAt                   time.Time

	// GroupRates 用户专属分组倍率配置
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64

	// Sora 存储配额
	SoraStorageQuotaBytes int64 // 用户级 Sora 存储配额（0 表示使用分组或系统默认值）
	SoraStorageUsedBytes  int64 // Sora 存储已用量

	// TOTP 双因素认证字段
	TotpSecretEncrypted *string    // AES-256-GCM 加密的 TOTP 密钥
	TotpEnabled         bool       // 是否启用 TOTP
	TotpEnabledAt       *time.Time // TOTP 启用时间

	APIKeys       []APIKey
	Subscriptions []UserSubscription
}

func (u *User) PaygDiscountRate() float64 {
	if u == nil {
		return 1
	}
	// A user-specific discount is an override, never an additional stacked
	// discount. Preserve legacy in-memory/cache objects that predate levels:
	// before the override flag existed, a non-default multiplier was the only
	// way to express an explicit user discount.
	if u.PaygDiscountOverrideEnabled || u.EffectiveAccessLevel() == nil ||
		(u.PaygDiscountMultiplier != nil && *u.PaygDiscountMultiplier != 1) {
		if u.PaygDiscountMultiplier != nil && *u.PaygDiscountMultiplier >= 0 && *u.PaygDiscountMultiplier <= 1 {
			return *u.PaygDiscountMultiplier
		}
	}
	if level := u.EffectiveAccessLevel(); level != nil && level.PaygDiscountMultiplier >= 0 && level.PaygDiscountMultiplier <= 1 {
		return level.PaygDiscountMultiplier
	}
	return 1
}

func (u *User) EffectiveAccessLevel() *AccessLevel {
	if u == nil {
		return nil
	}
	if u.ManualLevel != nil {
		return u.ManualLevel
	}
	return u.AutoLevel
}

func (u *User) MeetsRequiredLevel(required *AccessLevel) bool {
	if required == nil {
		return true
	}
	effective := u.EffectiveAccessLevel()
	return effective != nil && effective.Rank >= required.Rank
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

// CanBindGroup checks whether a user can bind to a given group.
// For standard groups:
// - Public groups (non-exclusive): all users can bind
// - Exclusive groups: only users with the group in AllowedGroups can bind
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	// 公开分组（非专属）：所有用户都可以绑定
	if !isExclusive {
		return true
	}
	// 专属分组：需要在 AllowedGroups 中
	for _, id := range u.AllowedGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

// CanAccessGroup combines the legacy public/exclusive entitlement with the
// group's level prerequisite. Explicit group grants and subscriptions do not
// bypass the level requirement.
func (u *User) CanAccessGroup(group *Group) bool {
	if group == nil || !u.MeetsRequiredLevel(group.RequiredLevel) {
		return false
	}
	return u.CanBindGroup(group.ID, group.IsExclusive)
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
