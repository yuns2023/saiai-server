package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_BillingRateMultiplier_DefaultsToOneWhenNil(t *testing.T) {
	var a Account
	require.NoError(t, json.Unmarshal([]byte(`{"id":1,"name":"acc","status":"active"}`), &a))
	require.Nil(t, a.RateMultiplier)
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}

func TestAccount_BillingRateMultiplier_AllowsZero(t *testing.T) {
	v := 0.0
	a := Account{RateMultiplier: &v}
	require.Equal(t, 0.0, a.BillingRateMultiplier())
}

func TestAccount_BillingRateMultiplier_NegativeFallsBackToOne(t *testing.T) {
	v := -1.0
	a := Account{RateMultiplier: &v}
	require.Equal(t, 1.0, a.BillingRateMultiplier())
}

func TestAccount_PaygDiscountRateAndValidation(t *testing.T) {
	require.Equal(t, 1.0, (&Account{}).PaygDiscountRate())
	zero := 0.0
	require.Equal(t, 0.0, (&Account{PaygDiscountMultiplier: &zero}).PaygDiscountRate())
	require.NoError(t, validatePaygDiscountMultiplier(&zero))
	tooPrecise := 0.12345
	require.Error(t, validatePaygDiscountMultiplier(&tooPrecise))
	aboveOne := 1.01
	require.Error(t, validatePaygDiscountMultiplier(&aboveOne))
}
