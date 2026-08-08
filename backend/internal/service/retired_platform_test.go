package service

import (
	"context"
	"errors"
	"testing"
)

func TestRetiredPlatformsAreNeverSchedulable(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformSora} {
		account := &Account{
			Platform:    platform,
			Status:      StatusActive,
			Schedulable: true,
		}
		if account.IsSchedulable() {
			t.Fatalf("retired platform %q must not be schedulable", platform)
		}
	}
}

func TestCreateRejectsRetiredPlatformsBeforeRepositoryAccess(t *testing.T) {
	svc := &adminServiceImpl{}

	for _, platform := range []string{PlatformAntigravity, PlatformSora} {
		if _, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: platform}); !errors.Is(err, ErrPlatformRetired) {
			t.Fatalf("CreateAccount(%q) error = %v, want ErrPlatformRetired", platform, err)
		}
		if _, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Platform: platform}); !errors.Is(err, ErrPlatformRetired) {
			t.Fatalf("CreateGroup(%q) error = %v, want ErrPlatformRetired", platform, err)
		}
	}
}
