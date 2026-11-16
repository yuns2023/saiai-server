//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const cryptoTestSigner = "SignerPubkey1111111111111111111111111111111"

var cryptoUserSeq uint64

// ---- fakes with the chain package signatures ----

func fakeDeriveEVM(xpub string, index uint32) (string, error) {
	if !strings.HasPrefix(xpub, "xpub") {
		return "", errors.New("not an xpub")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", xpub, index)))
	return "0x" + hex.EncodeToString(sum[:20]), nil
}

const cryptoB58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func fakeBase58(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var sb strings.Builder
	for i := 0; i < 44; i++ {
		_ = sb.WriteByte(cryptoB58[int(sum[i%32]+byte(i))%len(cryptoB58)])
	}
	return sb.String()
}

func fakeSolSig(seed string) string { return fakeBase58(seed+"a") + fakeBase58(seed+"b") }

func fakeVerifyManifest(raw []byte, trusted string) (*chain.SolanaPoolManifest, error) {
	var m chain.SolanaPoolManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.SignerPubkey != trusted || m.Signature == "" {
		return nil, errors.New("bad signature")
	}
	return &m, nil
}

func fakeATA(owner, mint, program string) (string, error) {
	return fakeBase58("ata|" + owner + "|" + mint + "|" + program), nil
}

type fakeEVMScanner struct {
	mu        sync.Mutex
	finalized uint64
	latest    uint64
	logs      []chain.Observation // returned when in range and address matches
	fail      error
	calls     []string
	onScan    func()
	txObs     []chain.Observation
	txErr     error
	finErr    error
	block     map[string]bool // ScanRange waits for ctx cancellation if any address is listed
	failFor   map[string]bool // ScanRange fails fast if any address is listed
}

func (f *fakeEVMScanner) Network() chain.Network { return chain.ArbitrumOne }
func (f *fakeEVMScanner) LatestBlock(context.Context) (uint64, error) {
	return f.latest, nil
}
func (f *fakeEVMScanner) FinalizedBlock(context.Context) (uint64, error) {
	return f.finalized, f.finErr
}
func (f *fakeEVMScanner) ScanRange(ctx context.Context, from, to uint64, addresses []string, finalized bool) ([]chain.Observation, error) {
	for _, a := range addresses {
		if f.block[a] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%d-%d/%v/%d", from, to, finalized, len(addresses)))
	for _, a := range addresses {
		if f.failFor[a] {
			return nil, errors.New("fake: batch fails")
		}
	}
	if f.onScan != nil {
		f.onScan()
	}
	if f.fail != nil {
		return nil, f.fail
	}
	set := map[string]bool{}
	for _, a := range addresses {
		set[a] = true
	}
	var out []chain.Observation
	for _, o := range f.logs {
		if o.BlockNumber >= from && o.BlockNumber <= to && set[o.ToAddress] && (!finalized || o.BlockNumber <= f.finalized) {
			o.Finalized = finalized
			out = append(out, o)
		}
	}
	return out, nil
}
func (f *fakeEVMScanner) ObservationsForTx(context.Context, string, []string, bool) ([]chain.Observation, error) {
	return f.txObs, f.txErr
}

var errFakeNotFinalized = errors.New("fake: not finalized yet")

type fakeSolanaScanner struct {
	txObs []chain.Observation
	mu    sync.Mutex
	obs   map[string][]chain.Observation // token account -> inflows (newest last)
	until []string
}

func (f *fakeSolanaScanner) Network() chain.Network { return chain.SolanaMainnet }
func (f *fakeSolanaScanner) ScanTokenAccount(_ context.Context, _, tokenAccount string, _ chain.Asset, untilSig, commitment string) ([]chain.Observation, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if commitment == "finalized" {
		f.until = append(f.until, tokenAccount+"<"+untilSig)
	}
	all := f.obs[tokenAccount]
	var out []chain.Observation
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].TxHash == untilSig {
			break
		}
		o := all[i]
		o.Finalized = commitment == "finalized"
		out = append(out, o)
	}
	newest := ""
	if len(all) > 0 {
		newest = all[len(all)-1].TxHash
	}
	return out, newest, nil
}
func (f *fakeSolanaScanner) ObservationsForTx(context.Context, string, []string, string) ([]chain.Observation, error) {
	return f.txObs, nil
}

// ---- harness ----

type cryptoEnv struct {
	svc      *service.CryptoPayService
	settings service.SettingRepository
	evm      *fakeEVMScanner
	sol      *fakeSolanaScanner
}

func resetCryptoTables(t *testing.T) {
	t.Helper()
	_, err := integrationDB.Exec(`TRUNCATE crypto_pay_credits, crypto_pay_deposits, crypto_pay_orders, crypto_pay_addresses,
		crypto_pay_solana_pool_entries, crypto_pay_keysets, crypto_pay_scan_cursors, crypto_pay_audit_logs RESTART IDENTITY`)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`DELETE FROM settings WHERE key LIKE 'crypto_pay_%'`)
	require.NoError(t, err)
}

func (e *cryptoEnv) set(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, e.settings.Set(context.Background(), key, value))
}

func newCryptoEnv(t *testing.T, withScanners bool) *cryptoEnv {
	t.Helper()
	return newCryptoEnvWith(t, withScanners, true, 60)
}

func newCryptoEnvWith(t *testing.T, withScanners, enabled bool, batchTimeoutSeconds int) *cryptoEnv {
	t.Helper()
	resetCryptoTables(t)
	// These tests commit real rows; remove them so suites that count users
	// (or delete them) are not affected.
	t.Cleanup(func() {
		resetCryptoTables(t)
		_, err := integrationDB.Exec(`DELETE FROM users WHERE email LIKE 'crypto-%@example.test'`)
		require.NoError(t, err)
	})
	env := &cryptoEnv{settings: NewSettingRepository(integrationEntClient)}
	env.set(t, service.SettingCryptoPayAllowNewAddresses, "true")
	deps := service.CryptoPayChainDeps{DeriveEVMAddress: fakeDeriveEVM, VerifySolanaManifest: fakeVerifyManifest, SolanaATA: fakeATA,
		IsRetryLater: func(err error) bool { return errors.Is(err, errFakeNotFinalized) }}
	if withScanners {
		env.evm = &fakeEVMScanner{finalized: 1000, latest: 1010}
		env.sol = &fakeSolanaScanner{obs: map[string][]chain.Observation{}}
		deps.EVMScanner, deps.SolanaScanner = env.evm, env.sol
	}
	env.svc = service.NewCryptoPayService(integrationDB, env.settings, config.CryptoPayConfig{
		Enabled: enabled, EVMNetwork: "arbitrum-one", SolanaNetwork: "solana-mainnet", SolanaPoolSignerPubkey: cryptoTestSigner,
		ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: batchTimeoutSeconds,
	}, deps, nil, nil)
	return env
}

func createCryptoUser(t *testing.T) int64 {
	t.Helper()
	n := atomic.AddUint64(&cryptoUserSeq, 1)
	var id int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO users (email, password_hash, balance) VALUES ($1, 'x', 0) RETURNING id`,
		fmt.Sprintf("crypto-%d-%d@example.test", time.Now().UnixNano(), n)).Scan(&id))
	return id
}

func userBalance(t *testing.T, userID int64) string {
	t.Helper()
	var b string
	require.NoError(t, integrationDB.QueryRow(`SELECT balance::text FROM users WHERE id = $1`, userID).Scan(&b))
	return b
}

func importTestEVMKeyset(t *testing.T, env *cryptoEnv, xpub string) {
	t.Helper()
	_, err := env.svc.ImportEVMKeyset(context.Background(), "arbitrum-one", xpub, "admin:test")
	require.NoError(t, err)
}

func testManifest(t *testing.T, seed string, n int, signer string) []byte {
	t.Helper()
	m := chain.SolanaPoolManifest{Version: 1, Network: chain.SolanaMainnet, PathTemplate: "m/44'/501'/{i}'/0'", SignerPubkey: signer, Signature: "sig"}
	for i := 0; i < n; i++ {
		m.Entries = append(m.Entries, chain.SolanaPoolEntry{Index: uint32(i), Address: fakeBase58(fmt.Sprintf("%s-%d", seed, i))})
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return raw
}

var cryptoTxSeq uint64

func evmObs(to string, units int64, block uint64, finalized bool) chain.Observation {
	n := atomic.AddUint64(&cryptoTxSeq, 1)
	sum := sha256.Sum256([]byte(fmt.Sprintf("tx-%d", n)))
	return chain.Observation{
		Network: chain.ArbitrumOne, Asset: chain.MainnetAssets()[0], TxHash: "0x" + hex.EncodeToString(sum[:]),
		EventKey: "log:0", ToAddress: to, Units: big.NewInt(units), BlockNumber: block,
		BlockHash: "0xblock", BlockTime: time.Now().Add(time.Minute), Finalized: finalized,
	}
}

func depositStatus(t *testing.T, network, txHash, eventKey string) string {
	t.Helper()
	var s string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM crypto_pay_deposits WHERE network = $1 AND tx_hash = $2 AND event_key = $3`,
		network, txHash, eventKey).Scan(&s))
	return s
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRow(query, args...).Scan(&n))
	return n
}

func evmAddressFor(t *testing.T, env *cryptoEnv, userID int64) string {
	t.Helper()
	addr, err := env.svc.EnsureAddress(context.Background(), userID, chain.ArbitrumOne)
	require.NoError(t, err)
	return addr.Address
}

// ---- tests ----

func TestCryptoPay_IngestIsIdempotentAndCreditsExactDecimals(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTEST1")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)

	a := evmObs(to, 505000, 10, true)
	b := evmObs(to, 505000, 11, true)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{a, a, b})
	require.NoError(t, err)
	require.Equal(t, 2, res.Inserted)
	_, err = env.svc.IngestObservations(ctx, []chain.Observation{a, b}) // retry of the whole batch
	require.NoError(t, err)
	require.Equal(t, "1.01000000", userBalance(t, user))
	require.Equal(t, 2, countRows(t, `SELECT COUNT(*) FROM crypto_pay_credits WHERE user_id = $1`, user))
	require.Equal(t, "credited", depositStatus(t, "arbitrum-one", a.TxHash, "log:0"))
	require.Equal(t, 2, countRows(t, `SELECT COUNT(*) FROM crypto_pay_audit_logs WHERE action = 'DEPOSIT_CREDITED' AND user_id = $1`, user))

	one := evmObs(to, 1, 12, true)
	_, err = env.svc.IngestObservations(ctx, []chain.Observation{one})
	require.NoError(t, err)
	require.Equal(t, "1.01000100", userBalance(t, user), "1 unit = 0.000001")

	// Unknown address and non-whitelisted contract are ignored without a row.
	stranger := evmObs("0x"+strings.Repeat("1", 40), 1000000, 13, true)
	fake := evmObs(to, 1000000, 13, true)
	fake.Asset.Contract = "0x" + strings.Repeat("2", 40)
	res, err = env.svc.IngestObservations(ctx, []chain.Observation{stranger, fake})
	require.NoError(t, err)
	require.Equal(t, 2, res.Ignored)
	require.Equal(t, 3, countRows(t, `SELECT COUNT(*) FROM crypto_pay_deposits`))

	rec, err := env.svc.Reconciliation(ctx)
	require.NoError(t, err)
	require.True(t, rec.Consistent)
	require.Equal(t, "1.010001", rec.CreditsTotal)
}

func TestCryptoPay_ConcurrentCreditOfSameDepositCreditsOnce(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	importTestEVMKeyset(t, env, "xpubTEST2")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)
	o := evmObs(to, 2_500_000, 20, true)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{o}) // auto credit off: stays finalized
	require.NoError(t, err)
	require.Len(t, res.DepositIDs, 1)
	require.Equal(t, "finalized", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")

	var credited int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				out, err := env.svc.ReleaseDeposit(ctx, res.DepositIDs[0], "admin:race")
				if err == nil && out.Credited {
					atomic.AddInt64(&credited, 1)
				}
				return
			}
			n, err := env.svc.CreditFinalizedBacklog(ctx)
			if err == nil {
				atomic.AddInt64(&credited, int64(n))
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int64(1), credited)
	require.Equal(t, "2.50000000", userBalance(t, user))
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_credits WHERE deposit_id = $1`, res.DepositIDs[0]))
}

func TestCryptoPay_HoldRulesAndRelease(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.set(t, service.SettingCryptoPayHoldThreshold, "5")
	importTestEVMKeyset(t, env, "xpubTEST3")

	// Amount at the threshold is held; release credits it.
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)
	large := evmObs(to, 5_000_000, 30, true)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{large})
	require.NoError(t, err)
	require.Equal(t, "held", depositStatus(t, "arbitrum-one", large.TxHash, "log:0"))
	require.Equal(t, "0.00000000", userBalance(t, user))
	n, err := env.svc.CreditFinalizedBacklog(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "held deposits are not auto-retried")
	out, err := env.svc.ReleaseDeposit(ctx, res.DepositIDs[0], "admin:1")
	require.NoError(t, err)
	require.True(t, out.Credited)
	require.Equal(t, "5.00000000", userBalance(t, user))
	out, err = env.svc.ReleaseDeposit(ctx, res.DepositIDs[0], "admin:1")
	require.NoError(t, err)
	require.False(t, out.Credited, "second release is a no-op")
	require.Equal(t, "5.00000000", userBalance(t, user))

	// Disabled user → held(user_inactive); release still credits (admin decision).
	disabled := createCryptoUser(t)
	toDisabled := evmAddressFor(t, env, disabled)
	_, err = integrationDB.Exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, disabled)
	require.NoError(t, err)
	small := evmObs(toDisabled, 1_000_000, 31, true)
	res, err = env.svc.IngestObservations(ctx, []chain.Observation{small})
	require.NoError(t, err)
	var reason string
	require.NoError(t, integrationDB.QueryRow(`SELECT hold_reason FROM crypto_pay_deposits WHERE id = $1`, res.DepositIDs[0]).Scan(&reason))
	require.Equal(t, service.CryptoPayHoldUserInactive, reason)

	// Deleted user → held; release refuses.
	deleted := createCryptoUser(t)
	toDeleted := evmAddressFor(t, env, deleted)
	_, err = integrationDB.Exec(`UPDATE users SET deleted_at = NOW() WHERE id = $1`, deleted)
	require.NoError(t, err)
	gone := evmObs(toDeleted, 1_000_000, 32, true)
	res, err = env.svc.IngestObservations(ctx, []chain.Observation{gone})
	require.NoError(t, err)
	require.Equal(t, "held", depositStatus(t, "arbitrum-one", gone.TxHash, "log:0"))
	_, err = env.svc.ReleaseDeposit(ctx, res.DepositIDs[0], "admin:1")
	require.Error(t, err)
	require.Equal(t, "CRYPTO_PAY_USER_DELETED", infraerrors.Reason(err))
	require.Equal(t, 0, countRows(t, `SELECT COUNT(*) FROM crypto_pay_credits WHERE deposit_id = $1`, res.DepositIDs[0]))
}

func TestCryptoPay_ObservedFinalizedTransitionsAndReorgOrphan(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTEST4")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)

	o := evmObs(to, 3_000_000, 100, false)
	_, err := env.svc.IngestObservations(ctx, []chain.Observation{o})
	require.NoError(t, err)
	require.Equal(t, "observed", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))
	require.Equal(t, "0.00000000", userBalance(t, user), "observed is never credited")

	// Not orphaned while the finalized cursor has not passed the block.
	_, err = integrationDB.Exec(`UPDATE crypto_pay_deposits SET updated_at = NOW() - INTERVAL '3 hours'`)
	require.NoError(t, err)
	n, err := env.svc.OrphanStaleObservations(ctx, chain.ArbitrumOne, 2*time.Hour)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = integrationDB.Exec(`INSERT INTO crypto_pay_scan_cursors (scope_key, network, position, version) VALUES ($1, 'arbitrum-one', '200', 1)`,
		"evm:arbitrum-one:fin:"+to)
	require.NoError(t, err)
	n, err = env.svc.OrphanStaleObservations(ctx, chain.ArbitrumOne, 2*time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_audit_logs WHERE action = 'DEPOSIT_ORPHANED'`))
	require.Equal(t, "orphaned", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))

	// The same event later seen finalized (re-included elsewhere) is credited.
	o2 := o
	o2.Finalized, o2.BlockNumber, o2.BlockHash = true, 150, "0xother"
	_, err = env.svc.IngestObservations(ctx, []chain.Observation{o2})
	require.NoError(t, err)
	require.Equal(t, "credited", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))
	require.Equal(t, "3.00000000", userBalance(t, user))

	// A credited row never goes back (stale observed or conflicting finalized observation).
	o3 := o
	o3.Finalized = false
	o4 := o2
	o4.Units = big.NewInt(9_000_000)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{o3, o4})
	require.NoError(t, err)
	require.Equal(t, 1, res.Conflicts)
	require.Equal(t, "credited", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))
	require.Equal(t, "3.00000000", userBalance(t, user))
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_audit_logs WHERE action = 'DEPOSIT_CONFLICT'`))
}

func TestCryptoPay_EVMAllocationConcurrencyNoDuplicateIndex(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	importTestEVMKeyset(t, env, "xpubTEST5")
	users := make([]int64, 20)
	for i := range users {
		users[i] = createCryptoUser(t)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for _, u := range users {
		for k := 0; k < 3; k++ { // the same user racing itself too
			wg.Add(1)
			go func(u int64) {
				defer wg.Done()
				if _, err := env.svc.EnsureAddress(ctx, u, chain.ArbitrumOne); err != nil {
					errs <- err
				}
			}(u)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 20, countRows(t, `SELECT COUNT(*) FROM crypto_pay_addresses`))
	require.Equal(t, 20, countRows(t, `SELECT COUNT(DISTINCT derivation_index) FROM crypto_pay_addresses`))
	require.Equal(t, 19, countRows(t, `SELECT MAX(derivation_index) FROM crypto_pay_addresses`), "no gaps: losers roll back their reservation")
	require.Equal(t, 20, countRows(t, `SELECT next_index FROM crypto_pay_keysets WHERE active`))

	// Re-requesting returns the same fixed address.
	a1, err := env.svc.EnsureAddress(ctx, users[0], chain.ArbitrumOne)
	require.NoError(t, err)
	a2, err := env.svc.EnsureAddress(ctx, users[0], chain.ArbitrumOne)
	require.NoError(t, err)
	require.Equal(t, a1.Address, a2.Address)

	// Rotating the xpub keeps old addresses; new users get index 0 of the new keyset.
	importTestEVMKeyset(t, env, "xpubTEST5b")
	_, err = env.svc.ImportEVMKeyset(ctx, "arbitrum-one", "xpubTEST5b", "admin:test")
	require.Error(t, err, "duplicate xpub import is rejected")
	nu := createCryptoUser(t)
	a3, err := env.svc.EnsureAddress(ctx, nu, chain.ArbitrumOne)
	require.NoError(t, err)
	want, _ := fakeDeriveEVM("xpubTEST5b", 0)
	require.Equal(t, want, a3.Address)
	a4, err := env.svc.EnsureAddress(ctx, users[0], chain.ArbitrumOne)
	require.NoError(t, err)
	require.Equal(t, a1.Address, a4.Address)

	_, err = env.svc.ImportEVMKeyset(ctx, "arbitrum-one", "notanxpub", "admin:test")
	require.Error(t, err)
	require.Equal(t, "CRYPTO_PAY_XPUB_INVALID", infraerrors.Reason(err))
}

func TestCryptoPay_SolanaPoolAllocationAndExhaustion(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)

	// No pool → 409.
	u0 := createCryptoUser(t)
	_, err := env.svc.EnsureAddress(ctx, u0, chain.SolanaMainnet)
	require.Error(t, err)
	require.Equal(t, service.CryptoPayReasonAddressUnavailable, infraerrors.Reason(err))
	require.Equal(t, 409, infraerrors.Code(err))

	_, err = env.svc.ImportSolanaKeyset(ctx, testManifest(t, "pool", 3, "WrongSigner11111111111111111111111111111111"), "admin:test")
	require.Error(t, err, "untrusted signer")
	raw := testManifest(t, "pool", 3, cryptoTestSigner)
	ks, err := env.svc.ImportSolanaKeyset(ctx, raw, "admin:test")
	require.NoError(t, err)
	require.Equal(t, int64(3), ks.PoolRemaining)
	_, err = env.svc.ImportSolanaKeyset(ctx, raw, "admin:test")
	require.Error(t, err)
	require.Equal(t, "CRYPTO_PAY_KEYSET_CONFLICT", infraerrors.Reason(err))

	users := make([]int64, 6)
	for i := range users {
		users[i] = createCryptoUser(t)
	}
	var wg sync.WaitGroup
	var ok, unavailable int64
	for _, u := range users {
		wg.Add(1)
		go func(u int64) {
			defer wg.Done()
			_, err := env.svc.EnsureAddress(ctx, u, chain.SolanaMainnet)
			switch {
			case err == nil:
				atomic.AddInt64(&ok, 1)
			case infraerrors.Reason(err) == service.CryptoPayReasonAddressUnavailable:
				atomic.AddInt64(&unavailable, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(u)
	}
	wg.Wait()
	require.Equal(t, int64(3), ok)
	require.Equal(t, int64(3), unavailable)
	require.Equal(t, 3, countRows(t, `SELECT COUNT(DISTINCT address) FROM crypto_pay_addresses WHERE network = 'solana-mainnet'`))
	require.Equal(t, 0, countRows(t, `SELECT COUNT(*) FROM crypto_pay_solana_pool_entries WHERE assigned_user_id IS NULL`))
	keysets, err := env.svc.ListKeysets(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), keysets[0].PoolRemaining)
	require.Equal(t, int64(3), keysets[0].Allocated)

	// A second pool extends capacity; existing users keep their addresses.
	_, err = env.svc.ImportSolanaKeyset(ctx, testManifest(t, "pool2", 2, cryptoTestSigner), "admin:test")
	require.NoError(t, err)
	_, err = env.svc.EnsureAddress(ctx, u0, chain.SolanaMainnet)
	require.NoError(t, err)
}

func TestCryptoPay_OrdersStatusAndUserLists(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTEST6")
	user := createCryptoUser(t)
	other := createCryptoUser(t)

	// An inflow before the order exists is not part of the order.
	to := evmAddressFor(t, env, user)
	early := evmObs(to, 7_000_000, 5, true)
	early.BlockTime = time.Now().Add(-time.Hour)
	_, err := env.svc.IngestObservations(ctx, []chain.Observation{early})
	require.NoError(t, err)

	order, err := env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "1.01")
	require.NoError(t, err)
	require.Equal(t, "pending", order.Status)
	require.Equal(t, "1.01", order.Amount)
	require.Equal(t, to, order.Address)
	require.Equal(t, "0", order.ReceivedAmount)
	require.NotNil(t, order.Deposits)
	require.Empty(t, order.Deposits)

	_, err = env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "0.5")
	require.Error(t, err, "below min_amount")
	_, err = env.svc.CreateOrder(ctx, user, "arbitrum-one", "DAI", "5")
	require.Error(t, err)
	_, err = env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "1.001")
	require.Equal(t, service.CryptoPayReasonAmountInvalid, infraerrors.Reason(err), "orders allow at most 2 decimals")
	_, err = env.svc.CreateOrder(ctx, user, "solana-devnet", "USDC", "5")
	require.Equal(t, service.CryptoPayReasonNetworkUnsupported, infraerrors.Reason(err))
	o25, err := env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDT", "25.5")
	require.NoError(t, err)
	require.Equal(t, "25.50", o25.Amount)

	pending := evmObs(to, 505000, 50, false)
	_, err = env.svc.IngestObservations(ctx, []chain.Observation{pending})
	require.NoError(t, err)
	order, err = env.svc.GetUserOrder(ctx, user, order.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", order.Status)
	require.Equal(t, "0", order.ReceivedAmount, "received counts finalized inflows only")
	require.Equal(t, "0.505", order.ConfirmingAmount)
	require.Equal(t, "confirming", order.Deposits[0].Status)
	require.Nil(t, order.Deposits[0].CreditedAt)

	fin := pending
	fin.Finalized = true
	second := evmObs(to, 505000, 51, true)
	second.Asset = chain.MainnetAssets()[1] // USDT counts toward a USDC order too
	_, err = env.svc.IngestObservations(ctx, []chain.Observation{fin, second})
	require.NoError(t, err)
	order, err = env.svc.GetUserOrder(ctx, user, order.ID)
	require.NoError(t, err)
	require.Equal(t, "paid", order.Status)
	require.Equal(t, "1.01", order.ReceivedAmount)
	require.Equal(t, "0", order.ConfirmingAmount)
	require.Equal(t, "1.01", order.CreditedAmount)
	require.Len(t, order.Deposits, 2)
	require.NotNil(t, order.Deposits[0].CreditedAt)

	_, err = env.svc.GetUserOrder(ctx, other, order.ID)
	require.Error(t, err)
	require.Equal(t, 404, infraerrors.Code(err))

	orders, total, err := env.svc.ListUserOrders(ctx, user, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, orders, 2)
	require.Equal(t, o25.ID, orders[0].ID, "newest first")
	deps, total, err := env.svc.ListUserDeposits(ctx, user, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(3), total, "deposits outside orders are listed too")
	require.Len(t, deps, 3)
	addrs, err := env.svc.ListUserAddresses(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []service.CryptoPayUserAddress{{Network: "arbitrum-one", Address: to}}, addrs)
	none, err := env.svc.ListUserAddresses(ctx, other)
	require.NoError(t, err)
	require.NotNil(t, none)
	require.Empty(t, none)
	require.Equal(t, "8.01000000", userBalance(t, user))

	// Switch off → 503 CRYPTO_PAY_DISABLED; history stays readable.
	env.set(t, service.SettingCryptoPayAllowNewAddresses, "false")
	_, err = env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "5")
	require.Equal(t, service.CryptoPayReasonDisabled, infraerrors.Reason(err))
	require.Equal(t, 503, infraerrors.Code(err))
	_, err = env.svc.GetUserOrder(ctx, user, order.ID)
	require.NoError(t, err)
}

func TestCryptoPay_WorkerScansWithCursorsAndCAS(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTEST7")
	user := createCryptoUser(t)

	env.evm.finalized = 900 // start height recorded at allocation
	_, err := env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "2")
	require.NoError(t, err)
	to := evmAddressFor(t, env, user)
	var start int64
	require.NoError(t, integrationDB.QueryRow(`SELECT start_block FROM crypto_pay_addresses WHERE address = $1`, to).Scan(&start))
	require.Equal(t, int64(900), start)

	env.evm.finalized, env.evm.latest = 1000, 1010
	env.evm.logs = []chain.Observation{evmObs(to, 2_000_000, 950, true), evmObs(to, 1_000_000, 1005, false), evmObs(to, 9_000_000, 899, true)}
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, "2.00000000", userBalance(t, user), "block 899 is before the start height; 1005 is unfinalized")
	require.Contains(t, env.evm.calls, "900-1000/true/1")
	require.Contains(t, env.evm.calls, "1001-1010/false/1")
	var pos string
	var version int64
	require.NoError(t, integrationDB.QueryRow(`SELECT position, version FROM crypto_pay_scan_cursors WHERE scope_key = $1`,
		"evm:arbitrum-one:fin:"+to).Scan(&pos, &version))
	require.Equal(t, "1000", pos)
	require.Equal(t, int64(1), version)
	require.Equal(t, "observed", depositStatus(t, "arbitrum-one", env.evm.logs[1].TxHash, "log:0"))

	// Next tick: block 1005 becomes final; the cursor resumes at 1001.
	env.evm.calls = nil
	env.evm.finalized, env.evm.latest = 1010, 1012
	env.svc.RunWorkerPass(ctx)
	require.Contains(t, env.evm.calls, "1001-1010/true/1")
	require.Equal(t, "3.00000000", userBalance(t, user))

	// A failing scan does not advance the cursor and records the error.
	env.evm.fail = errors.New("rpc down")
	env.evm.finalized = 1100
	env.svc.RunWorkerPass(ctx)
	var lastErr string
	require.NoError(t, integrationDB.QueryRow(`SELECT position, last_error FROM crypto_pay_scan_cursors WHERE scope_key = $1`,
		"evm:arbitrum-one:fin:"+to).Scan(&pos, &lastErr))
	require.Equal(t, "1010", pos)
	require.Contains(t, lastErr, "rpc down")

	// A concurrent writer bumping the cursor makes the CAS fail; its position wins.
	env.evm.fail = nil
	env.evm.onScan = func() {
		_, _ = integrationDB.Exec(`UPDATE crypto_pay_scan_cursors SET position = '1099', version = version + 10 WHERE scope_key = $1`, "evm:arbitrum-one:fin:"+to)
	}
	err = env.svc.ScanOnce(ctx)
	require.Error(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT position FROM crypto_pay_scan_cursors WHERE scope_key = $1`, "evm:arbitrum-one:fin:"+to).Scan(&pos))
	require.Equal(t, "1099", pos)

	// Scanning disabled → no RPC at all.
	env.evm.onScan = nil
	env.evm.calls = nil
	env.set(t, service.SettingCryptoPayScanningEnabled, "false")
	env.svc.RunWorkerPass(ctx)
	require.Empty(t, env.evm.calls)
}

func TestCryptoPay_SolanaScanPerTokenAccount(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	_, err := env.svc.ImportSolanaKeyset(ctx, testManifest(t, "solscan", 2, cryptoTestSigner), "admin:test")
	require.NoError(t, err)
	user := createCryptoUser(t)
	_, err = env.svc.CreateOrder(ctx, user, "solana-mainnet", "USDT", "1")
	require.NoError(t, err)
	addr, err := env.svc.EnsureAddress(ctx, user, chain.SolanaMainnet)
	require.NoError(t, err)
	usdt := chain.MainnetAssets()[3]
	ata, _ := fakeATA(addr.Address, usdt.Contract, usdt.TokenProgram)
	mk := func(seed string, units int64) chain.Observation {
		return chain.Observation{Network: chain.SolanaMainnet, Asset: usdt, TxHash: fakeSolSig(seed), EventKey: "acct:" + ata,
			ToAddress: addr.Address, TokenAccount: ata, Units: big.NewInt(units), BlockNumber: 5, BlockTime: time.Now().Add(time.Minute)}
	}
	first := mk("s1", 1_250_000)
	env.sol.obs[ata] = []chain.Observation{first}
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, "1.25000000", userBalance(t, user))
	second := mk("s2", 750_000)
	env.sol.obs[ata] = append(env.sol.obs[ata], second)
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, "2.00000000", userBalance(t, user))
	require.Contains(t, env.sol.until, ata+"<"+first.TxHash, "the walk resumes after the persisted signature")
	var pos string
	require.NoError(t, integrationDB.QueryRow(`SELECT position FROM crypto_pay_scan_cursors WHERE scope_key = $1`,
		"sol:solana-mainnet:USDT:"+ata).Scan(&pos))
	require.Equal(t, second.TxHash, pos)
}

func TestCryptoPay_RescanUsesChainAndSamePath(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	importTestEVMKeyset(t, env, "xpubTEST8")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)
	o := evmObs(to, 4_000_000, 700, true)
	unfinal := o
	unfinal.EventKey, unfinal.Finalized = "log:1", false
	env.evm.txObs = []chain.Observation{o, unfinal}

	_, err := env.svc.RescanTransaction(ctx, "arbitrum-one", strings.ToUpper(o.TxHash[2:]), "admin:9")
	require.Error(t, err, "hash without 0x is rejected")
	env.evm.txErr = fmt.Errorf("wrapped: %w", errFakeNotFinalized)
	_, err = env.svc.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:9")
	require.Equal(t, service.CryptoPayReasonTxNotReady, infraerrors.Reason(err), "not finalized is retry-later, never success")
	require.Equal(t, 409, infraerrors.Code(err))
	env.evm.txErr = errors.New("rpc down")
	_, err = env.svc.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:9")
	require.Equal(t, 503, infraerrors.Code(err))
	require.Equal(t, "0.00000000", userBalance(t, user))
	env.evm.txErr = nil
	res, err := env.svc.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:9")
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.True(t, res.Results[0].Credited)
	require.Equal(t, 1, res.Ignored)
	require.Equal(t, "4.00000000", userBalance(t, user))
	res, err = env.svc.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:9")
	require.NoError(t, err)
	require.False(t, res.Results[0].Credited, "rescan is idempotent")
	require.Equal(t, "4.00000000", userBalance(t, user))
	var operator string
	require.NoError(t, integrationDB.QueryRow(`SELECT operator FROM crypto_pay_credits LIMIT 1`).Scan(&operator))
	require.Equal(t, "admin:9", operator)

	deposits, total, err := env.svc.AdminListDeposits(ctx, service.CryptoPayAdminDepositFilter{Status: "credited"}, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "4000000", deposits[0].Units)
	require.Equal(t, "4", deposits[0].Amount)
	_, _, err = env.svc.AdminListDeposits(ctx, service.CryptoPayAdminDepositFilter{Status: "bogus"}, 1, 20)
	require.Error(t, err)
}

func TestCryptoPay_MigrationConstraints(t *testing.T) {
	env := newCryptoEnv(t, false)
	importTestEVMKeyset(t, env, "xpubTEST9")
	_, err := integrationDB.Exec(`INSERT INTO crypto_pay_keysets (network, family, version, xpub, path_template, fingerprint, active)
		VALUES ('arbitrum-one', 'evm', 99, 'xpubOther', 'm', 'fp-other', TRUE)`)
	require.Error(t, err, "only one active EVM keyset per network")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)
	var addressID int64
	require.NoError(t, integrationDB.QueryRow(`SELECT id FROM crypto_pay_addresses WHERE address = $1`, to).Scan(&addressID))
	insert := func(status string, units string) error {
		_, err := integrationDB.Exec(`INSERT INTO crypto_pay_deposits (network, tx_hash, event_key, address_id, user_id, address,
			asset_symbol, asset_contract, units, decimals, amount, block_number, block_time, status)
			VALUES ('arbitrum-one', $1, 'log:0', $2, $3, $4, 'USDC', '0x', $5::numeric, 6, 1, 1, NOW(), $6)`,
			"0x"+status+units, addressID, user, to, units, status)
		return err
	}
	require.Error(t, insert("bogus", "1"))
	require.Error(t, insert("observed", "0"))
	require.NoError(t, insert("observed", "115792089237316195423570985008687907853269984665640564039457584007913129639935"), "uint256 max fits NUMERIC(78,0)")
	var depositID int64
	require.NoError(t, integrationDB.QueryRow(`SELECT id FROM crypto_pay_deposits LIMIT 1`).Scan(&depositID))
	_, err = integrationDB.Exec(`INSERT INTO crypto_pay_credits (deposit_id, user_id, amount) VALUES ($1, $2, 1)`, depositID, user)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO crypto_pay_credits (deposit_id, user_id, amount) VALUES ($1, $2, 1)`, depositID, user)
	require.Error(t, err, "one ledger row per deposit")
}

func TestCryptoPay_SolanaManifestNetworkAndNonATARescan(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	// A devnet manifest (valid signature) is rejected on a mainnet deployment.
	m := chain.SolanaPoolManifest{Version: 1, Network: chain.SolanaDevnet, PathTemplate: "m/44'/501'/{i}'/0'", SignerPubkey: cryptoTestSigner, Signature: "sig",
		Entries: []chain.SolanaPoolEntry{{Index: 0, Address: fakeBase58("devnet-0")}}}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	_, err = env.svc.ImportSolanaKeyset(ctx, raw, "admin:test")
	require.Equal(t, service.CryptoPayReasonNetworkUnsupported, infraerrors.Reason(err))
	require.Equal(t, 0, countRows(t, `SELECT COUNT(*) FROM crypto_pay_keysets`))

	_, err = env.svc.ImportSolanaKeyset(ctx, testManifest(t, "nonata", 1, cryptoTestSigner), "admin:test")
	require.NoError(t, err)
	user := createCryptoUser(t)
	addr, err := env.svc.EnsureAddress(ctx, user, chain.SolanaMainnet)
	require.NoError(t, err)
	// Inflow to a non-ATA token account owned by the deposit address: only via rescan.
	other := fakeBase58("non-ata-account")
	sig := fakeSolSig("nonata-tx")
	env.sol.txObs = []chain.Observation{{Network: chain.SolanaMainnet, Asset: chain.MainnetAssets()[2], TxHash: sig, EventKey: "acct:" + other,
		ToAddress: addr.Address, TokenAccount: other, Units: big.NewInt(3_000_000), BlockNumber: 9, BlockTime: time.Now(), Finalized: true}}
	res, err := env.svc.RescanTransaction(ctx, "solana-mainnet", sig, "admin:2")
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.True(t, res.Results[0].Credited)
	require.Equal(t, "3.00000000", userBalance(t, user))
}

func TestCryptoPay_EVMAllocationNeedsStartHeightAndLaggingCatchUp(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	importTestEVMKeyset(t, env, "xpubTEST10")
	user := createCryptoUser(t)

	// Scanner wired but finalized height unavailable → no unwatched address is handed out.
	env.evm.finErr = errors.New("rpc down")
	_, err := env.svc.EnsureAddress(ctx, user, chain.ArbitrumOne)
	require.Equal(t, service.CryptoPayReasonAddressUnavailable, infraerrors.Reason(err))
	require.Equal(t, 0, countRows(t, `SELECT COUNT(*) FROM crypto_pay_addresses`))
	env.evm.finErr = nil

	// An inactive address far behind the head is scanned every tick until caught up.
	env.evm.finalized = 100
	to := evmAddressFor(t, env, user)
	env.evm.finalized, env.evm.latest = 500_000, 500_000
	env.svc.RunWorkerPass(ctx)
	env.svc.RunWorkerPass(ctx)
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, []string{"100-200099/true/1", "200100-400099/true/1", "400100-500000/true/1"}, env.evm.calls)
	var pos string
	require.NoError(t, integrationDB.QueryRow(`SELECT position FROM crypto_pay_scan_cursors WHERE scope_key = $1`, "evm:arbitrum-one:fin:"+to).Scan(&pos))
	require.Equal(t, "500000", pos)
	env.evm.calls = nil
	env.svc.RunWorkerPass(ctx)
	require.Empty(t, env.evm.calls, "caught up and inactive: next sweep within 23h")
}

func cursorPosition(t *testing.T, address string) (string, string) {
	t.Helper()
	var pos, lastErr string
	err := integrationDB.QueryRow(`SELECT position, last_error FROM crypto_pay_scan_cursors WHERE scope_key = $1`, "evm:arbitrum-one:fin:"+address).Scan(&pos, &lastErr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ""
	}
	require.NoError(t, err)
	return pos, lastErr
}

func allocateEVMAt(t *testing.T, env *cryptoEnv, finalized uint64) (int64, string) {
	t.Helper()
	env.evm.finalized = finalized
	u := createCryptoUser(t)
	return u, evmAddressFor(t, env, u)
}

func TestCryptoPay_EVMStuckBatchDoesNotStarveOthers(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnvWith(t, true, true, 1) // 1s per-batch budget
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubSTUCK")
	_, stuck := allocateEVMAt(t, env, 100)         // oldest batch: always times out
	user, healthy := allocateEVMAt(t, env, 50_000) // separate batch (spread > 10k blocks)
	env.evm.block = map[string]bool{stuck: true}
	env.evm.finalized, env.evm.latest = 60_000, 60_000
	env.evm.logs = []chain.Observation{evmObs(healthy, 1_000_000, 55_000, true)}

	for tick := 0; tick < 2; tick++ {
		env.svc.RunWorkerPass(ctx)
	}
	pos, _ := cursorPosition(t, healthy)
	require.Equal(t, "60000", pos, "healthy batch advanced despite a stuck older batch")
	require.Equal(t, "1.00000000", userBalance(t, user))
	pos, lastErr := cursorPosition(t, stuck)
	require.Empty(t, pos, "stuck batch never advances")
	require.Contains(t, lastErr, "deadline")
}

func TestCryptoPay_EVMFailingBatchesRotate(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	importTestEVMKeyset(t, env, "xpubROTATE")
	env.evm.failFor = map[string]bool{}
	for i := 0; i < 50; i++ { // 50 oldest batches fail fast every time
		_, a := allocateEVMAt(t, env, uint64(1_000+i*20_000))
		env.evm.failFor[a] = true
	}
	_, healthy := allocateEVMAt(t, env, 1_000+50*20_000)
	env.evm.finalized, env.evm.latest = 1_200_000, 1_200_000

	env.svc.RunWorkerPass(ctx)
	pos, _ := cursorPosition(t, healthy)
	require.Empty(t, pos, "first tick spends the 50-batch budget on the never-attempted failing batches")
	env.svc.RunWorkerPass(ctx)
	pos, _ = cursorPosition(t, healthy)
	require.Equal(t, "1200000", pos, "failed batches rotate to the back; the healthy one gets its turn")
}

func TestCryptoPay_InvalidKnownAssetObservationBlocksCursor(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubINVALID")
	user, to := allocateEVMAt(t, env, 900)
	bad := evmObs(to, 2_000_000, 950, true)
	bad.Asset.Decimals = 18 // whitelisted contract, wrong decimals
	env.evm.finalized, env.evm.latest = 1000, 1000
	env.evm.logs = []chain.Observation{bad}
	env.svc.RunWorkerPass(ctx)
	pos, lastErr := cursorPosition(t, to)
	require.Empty(t, pos, "cursor must not advance past an unprocessable known-asset transfer")
	require.Contains(t, lastErr, "decimals")
	require.Equal(t, 0, countRows(t, `SELECT COUNT(*) FROM crypto_pay_deposits`))
	rec, err := env.svc.Reconciliation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rec.CursorErrors)

	// Unknown address / non-whitelisted asset stay ignorable.
	fake := evmObs(to, 5, 951, true)
	fake.Asset.Contract = "0x" + strings.Repeat("3", 40)
	good := bad
	good.Asset = chain.MainnetAssets()[0]
	env.evm.logs = []chain.Observation{good, fake, evmObs("0x"+strings.Repeat("4", 40), 7, 952, true)}
	env.svc.RunWorkerPass(ctx)
	pos, _ = cursorPosition(t, to)
	require.Equal(t, "1000", pos)
	require.Equal(t, "2.00000000", userBalance(t, user))
}

func TestCryptoPay_ThreeWayCreditRace(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	importTestEVMKeyset(t, env, "xpubRACE3")
	user, to := allocateEVMAt(t, env, 1)
	o := evmObs(to, 4_200_000, 10, true)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{o})
	require.NoError(t, err)
	id := res.DepositIDs[0]
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.evm.txObs = []chain.Observation{o}
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				_, _ = env.svc.CreditFinalizedBacklog(ctx)
			case 1:
				_, _ = env.svc.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:rescan")
			default:
				_, _ = env.svc.ReleaseDeposit(ctx, id, "admin:release")
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, "4.20000000", userBalance(t, user))
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_credits`))
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_audit_logs WHERE action = 'DEPOSIT_CREDITED'`))
}

func TestCryptoPay_CrashAfterIngestBeforeCursorIsIdempotent(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayScanningEnabled, "true")
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	importTestEVMKeyset(t, env, "xpubCRASH")
	user, to := allocateEVMAt(t, env, 100)
	_, err := integrationDB.Exec(`UPDATE crypto_pay_addresses SET last_order_at = NOW() WHERE address = $1`, to) // active: scanned every tick
	require.NoError(t, err)
	env.evm.finalized, env.evm.latest = 200, 200
	env.svc.RunWorkerPass(ctx) // cursor at 200, nothing found
	env.evm.finalized, env.evm.latest = 300, 300
	env.evm.logs = []chain.Observation{evmObs(to, 1_500_000, 250, true)}
	scope := "evm:arbitrum-one:fin:" + to
	env.evm.onScan = func() { // another writer wins the cursor between ingest and CAS
		_, _ = integrationDB.Exec(`UPDATE crypto_pay_scan_cursors SET version = version + 5 WHERE scope_key = $1`, scope)
	}
	env.svc.RunWorkerPass(ctx)
	pos, _ := cursorPosition(t, to)
	require.Equal(t, "200", pos, "CAS failed: cursor not advanced")
	require.Equal(t, "1.50000000", userBalance(t, user), "ingest already credited")
	env.evm.onScan = nil
	env.svc.RunWorkerPass(ctx) // same range again
	pos, _ = cursorPosition(t, to)
	require.Equal(t, "300", pos)
	require.Equal(t, "1.50000000", userBalance(t, user), "re-scan does not credit twice")
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_credits`))
}

func TestCryptoPay_AutoCreditOffThenOnWithScanningOff(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	importTestEVMKeyset(t, env, "xpubBACKLOG")
	user := createCryptoUser(t)
	to := evmAddressFor(t, env, user)
	_, err := env.svc.IngestObservations(ctx, []chain.Observation{evmObs(to, 600_000, 5, true)})
	require.NoError(t, err)
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, "0.00000000", userBalance(t, user), "auto credit off")
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.set(t, service.SettingCryptoPayScanningEnabled, "false")
	env.svc.RunWorkerPass(ctx)
	require.Equal(t, "0.60000000", userBalance(t, user), "backlog credits without scanning")
}

func TestCryptoPay_DisabledDeploymentNeverMovesMoney(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, true)
	env.set(t, service.SettingCryptoPayAutoCreditEnabled, "true")
	env.set(t, service.SettingCryptoPayHoldThreshold, "1")
	importTestEVMKeyset(t, env, "xpubMASTER")
	user, to := allocateEVMAt(t, env, 1)
	o := evmObs(to, 3_000_000, 5, true)
	res, err := env.svc.IngestObservations(ctx, []chain.Observation{o})
	require.NoError(t, err)
	require.Equal(t, "held", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))

	off := service.NewCryptoPayService(integrationDB, env.settings, config.CryptoPayConfig{Enabled: false},
		service.CryptoPayChainDeps{EVMScanner: env.evm}, nil, nil)
	_, err = off.ReleaseDeposit(ctx, res.DepositIDs[0], "admin:1")
	require.Equal(t, service.CryptoPayReasonDisabled, infraerrors.Reason(err))
	_, err = off.RescanTransaction(ctx, "arbitrum-one", o.TxHash, "admin:1")
	require.Equal(t, service.CryptoPayReasonDisabled, infraerrors.Reason(err))
	_, err = off.IngestObservations(ctx, []chain.Observation{o})
	require.Error(t, err)
	n, err := off.CreditFinalizedBacklog(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, "held", depositStatus(t, "arbitrum-one", o.TxHash, "log:0"))
	require.Equal(t, "0.00000000", userBalance(t, user))
}

func TestCryptoPay_OrderDepositsTruncatedAndSettingsAudit(t *testing.T) {
	ctx := context.Background()
	env := newCryptoEnv(t, false)
	importTestEVMKeyset(t, env, "xpubTRUNC")
	user := createCryptoUser(t)
	order, err := env.svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "5")
	require.NoError(t, err)
	require.False(t, order.DepositsTruncated)
	var addressID int64
	require.NoError(t, integrationDB.QueryRow(`SELECT id FROM crypto_pay_addresses WHERE user_id = $1`, user).Scan(&addressID))
	_, err = integrationDB.Exec(`
		INSERT INTO crypto_pay_deposits (network, tx_hash, event_key, address_id, user_id, address, asset_symbol, asset_contract,
			units, decimals, amount, block_number, block_time, status)
		SELECT 'arbitrum-one', 'trunc' || g, 'log:0', $1, $2, $3, 'USDC', 'x', 1000000, 6, 1, g, NOW() + INTERVAL '1 minute', 'finalized'
		FROM generate_series(1, 1001) g`, addressID, user, order.Address)
	require.NoError(t, err)
	order, err = env.svc.GetUserOrder(ctx, user, order.ID)
	require.NoError(t, err)
	require.True(t, order.DepositsTruncated)
	require.Len(t, order.Deposits, 1000)
	require.Equal(t, "1001", order.ReceivedAmount, "totals cover every inflow")
	require.Equal(t, "paid", order.Status)

	threshold := "250"
	out, err := env.svc.UpdateSettings(ctx, service.UpdateCryptoPaySettingsRequest{HoldThresholdAmount: &threshold}, "admin:7")
	require.NoError(t, err)
	require.Equal(t, "250", out.HoldThresholdAmount)
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM crypto_pay_audit_logs WHERE action = 'SETTINGS_UPDATED' AND operator = 'admin:7'`))
}
