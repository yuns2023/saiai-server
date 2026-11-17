//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// cryptoTopUpUser creates a crypto test user and removes its redeem codes and payment orders
// before newCryptoEnv's cleanup deletes the user (cleanups run last-in, first-out, so call
// newCryptoEnv first).
func cryptoTopUpUser(t *testing.T) int64 {
	t.Helper()
	id := createCryptoUser(t)
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM redeem_codes WHERE used_by = $1", id)
		require.NoError(t, err)
		_, err = integrationDB.Exec("DELETE FROM payment_orders WHERE user_id = $1", id)
		require.NoError(t, err)
	})
	return id
}

func ingestOne(t *testing.T, env *cryptoEnv, o chain.Observation) (int64, string) {
	t.Helper()
	res, err := env.svc.IngestObservations(context.Background(), []chain.Observation{o})
	require.NoError(t, err)
	require.Len(t, res.DepositIDs, 1)
	return res.DepositIDs[0], depositStatus(t, string(o.Network), o.TxHash, o.EventKey)
}

func TestTopUpChecks_CountCryptoOnlyOnceCredited(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayHoldThreshold, "5")
	importTestEVMKeyset(t, env, "xpubTOPUP1")
	inviteRepo := NewUserInviteRepository(integrationEntClient)
	redeemRepo := NewRedeemCodeRepository(integrationEntClient)
	user := cryptoTopUpUser(t)
	to := evmAddressFor(t, env, user)
	check := func(wantEligible bool, wantSum float64, msg string) {
		t.Helper()
		eligible, err := inviteRepo.HasQualifyingTopUp(ctx, user)
		require.NoError(t, err)
		require.Equal(t, wantEligible, eligible, msg)
		sum, err := redeemRepo.SumPositiveBalanceByUser(ctx, user)
		require.NoError(t, err)
		require.Equal(t, wantSum, sum, msg)
	}
	check(false, 0, "new user")

	// Auto credit is off: a deposit still confirming and a finalized one have moved no money.
	_, status := ingestOne(t, env, evmObs(to, 1_000_000, 10, false))
	require.Equal(t, "observed", status)
	_, status = ingestOne(t, env, evmObs(to, 1_500_000, 11, true))
	require.Equal(t, "finalized", status)
	check(false, 0, "observed and finalized deposits are not top-ups")

	// Auto credit on. Another user's credit neither qualifies nor counts for this user.
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	other := cryptoTopUpUser(t)
	_, status = ingestOne(t, env, evmObs(evmAddressFor(t, env, other), 4_000_000, 12, true))
	require.Equal(t, "credited", status)
	check(false, 0, "another user's credit")

	// An amount at the hold threshold is held, a smaller one is credited.
	heldID, status := ingestOne(t, env, evmObs(to, 7_000_000, 12, true))
	require.Equal(t, "held", status)
	check(false, 0, "a held deposit is not a top-up")

	_, status = ingestOne(t, env, evmObs(to, 2_500_000, 13, true))
	require.Equal(t, "credited", status)
	check(true, 2.5, "a credited deposit alone makes the user eligible and is summed")

	out, err := env.svc.ReleaseDeposit(ctx, heldID, "admin:test")
	require.NoError(t, err)
	require.True(t, out.Credited)
	check(true, 9.5, "a released deposit counts once credited")
	require.Equal(t, "9.50000000", userBalance(t, user), "the total matches what reached the balance")
}

func TestTopUpChecks_PaymentOrderCountsOnceNextToCodesAndCrypto(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTOPUP2")
	redeemRepo := NewRedeemCodeRepository(integrationEntClient)
	user := cryptoTopUpUser(t)

	// A fulfilled native payment order: COMPLETED, and its balance code (code = recharge code,
	// value = order amount) redeemed by the payer, as PaymentService.ensureOrderRedeemed leaves it.
	stamp := time.Now().UnixNano()
	provider, err := integrationEntClient.PaymentProviderInstance.Create().
		SetProviderKey("test").
		SetName(fmt.Sprintf("topup-test-%d", stamp)).
		SetConfigEncrypted("x").
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM payment_orders WHERE provider_instance_id = $1", provider.ID)
		require.NoError(t, err)
		_, err = integrationDB.Exec("DELETE FROM payment_provider_instances WHERE id = $1", provider.ID)
		require.NoError(t, err)
	})
	rechargeCode := fmt.Sprintf("PAYTOPUP%d", stamp)
	order, err := integrationEntClient.PaymentOrder.Create().
		SetUserID(user).
		SetUserEmail(fmt.Sprintf("crypto-order-%d@example.test", stamp)).
		SetAmount(10).
		SetPayAmount(10).
		SetRechargeCode(rechargeCode).
		SetOutTradeNo(fmt.Sprintf("OTOPUP%d", stamp)).
		SetPaymentType("alipay").
		SetProviderKey("test").
		SetProviderInstanceID(provider.ID).
		SetProviderSnapshotEncrypted("x").
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetStatus(payment.OrderStatusCompleted).
		Save(ctx)
	require.NoError(t, err)
	now := time.Now()
	mustCreateRedeemCode(t, integrationEntClient, &service.RedeemCode{
		Code: rechargeCode, Type: service.RedeemTypeBalance, Value: order.Amount, Status: service.StatusUsed,
		UsedBy: &user, UsedAt: &now, Notes: fmt.Sprintf("native payment order:%d", order.ID),
	})
	sum, err := redeemRepo.SumPositiveBalanceByUser(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 10.0, sum, "a paid order is counted once, through its redeem code")

	// Crypto credit plus other code types: only money top-ups are summed.
	_, status := ingestOne(t, env, evmObs(evmAddressFor(t, env, user), 2_500_000, 20, true))
	require.Equal(t, "credited", status)
	usedCode(t, user, domain.AdjustmentTypeAdminBalance, 3)
	usedCode(t, user, service.RedeemTypeBalance, -4) // deduction
	usedCode(t, user, service.RedeemTypeConcurrency, 1)
	sum, err = redeemRepo.SumPositiveBalanceByUser(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 15.5, sum, "order code 10 + crypto 2.5 + admin top-up 3")

	// The two sources are added without float noise.
	small := cryptoTopUpUser(t)
	usedCode(t, small, service.RedeemTypeBalance, 0.1)
	_, status = ingestOne(t, env, evmObs(evmAddressFor(t, env, small), 200_000, 21, true))
	require.Equal(t, "credited", status)
	sum, err = redeemRepo.SumPositiveBalanceByUser(ctx, small)
	require.NoError(t, err)
	require.Equal(t, 0.3, sum)

	// The code SUM below has 20 significant digits, more than a float64 holds: it must reach the
	// final float64 conversion as an exact decimal. (Each code value alone is float-exact.)
	large := cryptoTopUpUser(t)
	usedCode(t, large, service.RedeemTypeBalance, 123456789012.3456)
	usedCode(t, large, service.RedeemTypeBalance, 0.00007891)
	var codeSum string
	require.NoError(t, integrationDB.QueryRow("SELECT SUM(value)::text FROM redeem_codes WHERE used_by = $1", large).Scan(&codeSum))
	require.Equal(t, "123456789012.34567891", codeSum)
	_, status = ingestOne(t, env, evmObs(evmAddressFor(t, env, large), 2, 22, true)) // 0.000002
	require.Equal(t, "credited", status)
	sum, err = redeemRepo.SumPositiveBalanceByUser(ctx, large)
	require.NoError(t, err)
	want := decimal.RequireFromString(codeSum).Add(decimal.RequireFromString("0.000002")).InexactFloat64()
	require.Equal(t, want, sum)
}
