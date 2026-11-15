package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// lookupInvitationCode returns an unused invitation code that may be used to sign up,
// or ErrInvitationCodeInvalid. Every sign-up path (email, LinuxDo, Google/GitHub) uses
// this so they agree on what "valid" means. The caller still consumes the code with
// RedeemCodeRepository.Use inside the user-creation transaction; this check is advisory
// against concurrent use but authoritative for type and owner.
func (s *AuthService) lookupInvitationCode(ctx context.Context, code string) (*RedeemCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrInvitationCodeRequired
	}
	if s.redeemRepo == nil {
		return nil, ErrServiceUnavailable
	}
	redeemCode, err := s.redeemRepo.GetByCode(ctx, code)
	if err != nil || redeemCode == nil {
		return nil, ErrInvitationCodeInvalid
	}
	if redeemCode.Type != RedeemTypeInvitation || redeemCode.Status != StatusUnused {
		return nil, ErrInvitationCodeInvalid
	}
	ok, err := s.InvitationOwnerUsable(ctx, redeemCode)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvitationCodeInvalid
	}
	return redeemCode, nil
}

// InvitationOwnerUsable reports whether an invitation code's owner still allows it to be
// used. Admin codes (no owner) always do. A user-generated code stops working once its
// owner is deleted or no longer active, so disabling an abusive account also disables
// the codes it handed out.
func (s *AuthService) InvitationOwnerUsable(ctx context.Context, code *RedeemCode) (bool, error) {
	if code == nil || code.OwnerUserID == nil {
		return true, nil
	}
	if s.userRepo == nil {
		return false, ErrServiceUnavailable
	}
	owner, err := s.userRepo.GetByID(ctx, *code.OwnerUserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return false, nil
		}
		logger.LegacyPrintf("service.auth", "[Auth] Failed to load invitation owner %d: %v", *code.OwnerUserID, err)
		return false, ErrServiceUnavailable
	}
	return owner.IsActive(), nil
}
