//go:build unit

package service

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type cryptoPayFakeSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *cryptoPayFakeSettings) Get(ctx context.Context, key string) (*Setting, error) {
	v, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: v}, nil
}
func (s *cryptoPayFakeSettings) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (s *cryptoPayFakeSettings) Set(ctx context.Context, key, value string) error {
	return s.SetMultiple(ctx, map[string]string{key: value})
}
func (s *cryptoPayFakeSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *cryptoPayFakeSettings) SetMultiple(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	for k, v := range values {
		s.values[k] = v
	}
	return nil
}
func (s *cryptoPayFakeSettings) GetAll(ctx context.Context) (map[string]string, error) {
	return s.GetMultiple(ctx, cryptoPaySettingKeys)
}
func (s *cryptoPayFakeSettings) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func TestCryptoPayUnitsToAmountIsExact(t *testing.T) {
	cases := []struct {
		units string
		want  string
	}{
		{"1", "0.000001"},
		{"505000", "0.505"},
		{"1000000", "1"},
		{"123456789", "123.456789"},
		{"999999999999999999", "999999999999.999999"},
	}
	for _, c := range cases {
		u, ok := new(big.Int).SetString(c.units, 10)
		require.True(t, ok)
		got := cryptoPayUnitsToAmount(decimal.NewFromBigInt(u, 0), 6)
		require.Equal(t, c.want, formatCryptoAmount(got), c.units)
	}
	sum := cryptoPayUnitsToAmount(decimal.NewFromInt(505000), 6).Add(cryptoPayUnitsToAmount(decimal.NewFromInt(505000), 6))
	require.Equal(t, "1.01", formatCryptoAmount(sum))
	require.True(t, sum.Equal(decimal.RequireFromString("1.01")))
}

func TestComputeCryptoPayOrderStatus(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	amount := decimal.RequireFromString("10")
	require.Equal(t, CryptoPayOrderPending, computeCryptoPayOrderStatus(amount, decimal.RequireFromString("9.999999"), now.Add(time.Minute), now))
	require.Equal(t, CryptoPayOrderPaid, computeCryptoPayOrderStatus(amount, decimal.RequireFromString("10"), now.Add(time.Minute), now))
	require.Equal(t, CryptoPayOrderPaid, computeCryptoPayOrderStatus(amount, decimal.RequireFromString("12"), now.Add(-time.Minute), now), "paid wins over expired")
	require.Equal(t, CryptoPayOrderExpired, computeCryptoPayOrderStatus(amount, decimal.Zero, now, now))
}

func TestCryptoPayPublicDepositStatus(t *testing.T) {
	require.Equal(t, "confirming", cryptoPayPublicDepositStatus(cryptoDepositObserved))
	require.Equal(t, "confirming", cryptoPayPublicDepositStatus(cryptoDepositFinalized))
	require.Equal(t, "credited", cryptoPayPublicDepositStatus(cryptoDepositCredited))
	require.Equal(t, "held", cryptoPayPublicDepositStatus(cryptoDepositHeld))
	require.Equal(t, "orphaned", cryptoPayPublicDepositStatus(cryptoDepositOrphaned))
}

func TestParseCryptoPayNetworks(t *testing.T) {
	nets, warns := parseCryptoPayNetworks(config.CryptoPayConfig{})
	require.Empty(t, nets, "disabled by default")
	require.Empty(t, warns)

	nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-one", SolanaNetwork: "solana-mainnet"})
	require.Empty(t, warns)
	require.Len(t, nets, 2)
	require.Equal(t, chain.ArbitrumOne, nets[0].Network)
	require.Len(t, nets[0].Assets, 2)
	require.Equal(t, "0xaf88d065e77c8cc2239327c5edb3a432268e5831", nets[0].Assets[0].Contract)
	require.Equal(t, chain.SolanaMainnet, nets[1].Network)
	require.Equal(t, chain.SolanaTokenProgram, nets[1].Assets[0].TokenProgram)

	// Fail-closed: any invalid value disables the WHOLE feature, not just that network.
	nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-one",
		EVMAssets: "USDC:0x0000000000000000000000000000000000000001:6", SolanaNetwork: "solana-devnet"})
	require.Empty(t, nets)
	require.Len(t, warns, 3)
	nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60,
		EVMNetwork: "arbitrum-one", SolanaNetwork: "solana-mainnt"})
	require.Empty(t, nets, "a typo in one network disables both")
	require.NotEmpty(t, warns)
	nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 0, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-one"})
	require.Empty(t, nets)
	require.NotEmpty(t, warns)
	nets, _ = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 0, EVMNetwork: "arbitrum-one"})
	require.Empty(t, nets)
	svc := NewCryptoPayService(nil, &cryptoPayFakeSettings{}, config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60,
		EVMNetwork: "arbitrum-one", SolanaNetwork: "bogus"}, CryptoPayChainDeps{}, nil, nil)
	require.False(t, svc.Enabled())
	worker := NewCryptoPayWorker(nil, svc, time.Second)
	worker.Start()
	worker.Stop() // never started: returns immediately

	nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-sepolia",
		EVMAssets: "usdc:0x75FAF114EAFB1BDBE2F0316DF893FD58CE46AA4D:6", SolanaNetwork: ""})
	require.Empty(t, warns)
	require.Len(t, nets, 1)
	require.Equal(t, "USDC", nets[0].Assets[0].Symbol)
	require.Equal(t, "0x75faf114eafb1bdbe2f0316df893fd58ce46aa4d", nets[0].Assets[0].Contract)

	for _, bad := range []string{"DAI:0x75faf114eafb1bdbe2f0316df893fd58ce46aa4d:6", "USDC:0x1234:6", "USDC:0x75faf114eafb1bdbe2f0316df893fd58ce46aa4d:18", "USDC"} {
		nets, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-sepolia", EVMAssets: bad})
		require.Empty(t, nets, bad)
		require.Len(t, warns, 2, bad)
	}
	_, warns = parseCryptoPayNetworks(config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "ethereum"})
	require.Len(t, warns, 2)
}

func newCryptoPayUnitService(t *testing.T, values map[string]string) *CryptoPayService {
	t.Helper()
	return NewCryptoPayService(nil, &cryptoPayFakeSettings{values: values},
		config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, ScanUnitsPerPass: 20, SolanaPageSize: 200, ConfirmingUnitsPerPass: 5, EVMNetwork: "arbitrum-one", SolanaNetwork: "solana-mainnet"}, CryptoPayChainDeps{}, nil, nil)
}

func TestCryptoPaySettingsDefaultsAndValidation(t *testing.T) {
	svc := newCryptoPayUnitService(t, nil)
	st, err := svc.GetSettings(context.Background())
	require.NoError(t, err)
	require.False(t, st.AllowNewAddresses)
	require.False(t, st.ScanningEnabled)
	require.False(t, st.AutoCreditEnabled)
	require.Equal(t, "1", st.MinAmount.String())
	require.Equal(t, "10000", st.MaxAmount.String())
	require.Equal(t, 180, st.OrderTTLMinutes)
	require.Equal(t, "5000", st.HoldThresholdAmount.String())

	// Invalid stored values fall back to defaults.
	svc = newCryptoPayUnitService(t, map[string]string{
		SettingCryptoPayMinAmount: "-1", SettingCryptoPayHoldThreshold: "1.123456789", SettingCryptoPayOrderTTLMinutes: "abc",
		SettingCryptoPayAllowNewAddresses: "TRUE",
	})
	st, err = svc.GetSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1", st.MinAmount.String())
	require.Equal(t, "5000", st.HoldThresholdAmount.String())
	require.Equal(t, 180, st.OrderTTLMinutes)
	require.False(t, st.AllowNewAddresses, "only the exact string true enables a switch")
	require.ElementsMatch(t, []string{SettingCryptoPayMinAmount, SettingCryptoPayHoldThreshold, SettingCryptoPayOrderTTLMinutes, SettingCryptoPayAllowNewAddresses}, st.Invalid)

	// One invalid value forces every switch off (fail-closed).
	svc = newCryptoPayUnitService(t, map[string]string{
		SettingCryptoPayAllowNewAddresses: "true", SettingCryptoPayScanningEnabled: "true", SettingCryptoPayAutoCreditEnabled: "true",
		SettingCryptoPayMaxAmount: "abc",
	})
	st, err = svc.GetSettings(context.Background())
	require.NoError(t, err)
	require.False(t, st.AllowNewAddresses || st.ScanningEnabled || st.AutoCreditEnabled)
	cfg, err := svc.GetUserConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	// A present-but-empty value is invalid; only an absent key is a default.
	svc = newCryptoPayUnitService(t, map[string]string{SettingCryptoPayAutoCreditEnabled: "true", SettingCryptoPayOrderTTLMinutes: ""})
	st, err = svc.GetSettings(context.Background())
	require.NoError(t, err)
	require.False(t, st.AutoCreditEnabled)
	require.Equal(t, []string{SettingCryptoPayOrderTTLMinutes}, st.Invalid)
	// Missing values are just defaults.
	svc = newCryptoPayUnitService(t, map[string]string{SettingCryptoPayAutoCreditEnabled: "true"})
	st, err = svc.GetSettings(context.Background())
	require.NoError(t, err)
	require.True(t, st.AutoCreditEnabled)
	require.Empty(t, st.Invalid)
}

func TestCryptoPayUserConfigDisabledHasEmptyNetworks(t *testing.T) {
	svc := newCryptoPayUnitService(t, nil)
	cfg, err := svc.GetUserConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.NotNil(t, cfg.Networks)
	require.Empty(t, cfg.Networks)

	svc = newCryptoPayUnitService(t, map[string]string{SettingCryptoPayAllowNewAddresses: "true"})
	cfg, err = svc.GetUserConfig(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Networks, 2)
	require.Equal(t, "arbitrum-one", cfg.Networks[0].Network)
	require.Equal(t, "https://arbiscan.io/tx/{tx}", cfg.Networks[0].ExplorerTxURL)
	require.Equal(t, 20, cfg.Networks[0].FinalityHintMinutes)
	require.Equal(t, "https://solscan.io/account/{addr}", cfg.Networks[1].ExplorerAddressURL)
	require.Equal(t, 1, cfg.Networks[1].FinalityHintMinutes)

	// Deployment switch off: enabled stays false even with the setting on.
	off := NewCryptoPayService(nil, &cryptoPayFakeSettings{values: map[string]string{SettingCryptoPayAllowNewAddresses: "true"}},
		config.CryptoPayConfig{}, CryptoPayChainDeps{}, nil, nil)
	cfg, err = off.GetUserConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Networks)
	_, err = off.CreateOrder(context.Background(), 1, "arbitrum-one", "USDC", "10")
	require.Error(t, err)
	require.Contains(t, err.Error(), CryptoPayReasonDisabled)
}

func TestCryptoPayUpdateSettingsValidation(t *testing.T) {
	store := &cryptoPayFakeSettings{}
	svc := NewCryptoPayService(nil, store, config.CryptoPayConfig{}, CryptoPayChainDeps{}, nil, nil)
	bad := "abc"
	_, err := svc.UpdateSettings(context.Background(), UpdateCryptoPaySettingsRequest{MinAmount: &bad}, "admin:1")
	require.Error(t, err)
	tooPrecise := "1.000000001"
	_, err = svc.UpdateSettings(context.Background(), UpdateCryptoPaySettingsRequest{HoldThresholdAmount: &tooPrecise}, "admin:1")
	require.Error(t, err)
	minV, maxV := "100", "50"
	_, err = svc.UpdateSettings(context.Background(), UpdateCryptoPaySettingsRequest{MinAmount: &minV, MaxAmount: &maxV}, "admin:1")
	require.Error(t, err)
	ttl := 0
	_, err = svc.UpdateSettings(context.Background(), UpdateCryptoPaySettingsRequest{OrderTTLMinutes: &ttl}, "admin:1")
	require.Error(t, err)
	require.Empty(t, store.values, "rejected updates store nothing")
}

func TestCryptoPayNormalizeObservationRejectsUntrusted(t *testing.T) {
	svc := newCryptoPayUnitService(t, nil)
	usdc := chain.MainnetAssets()[0]
	good := chain.Observation{
		Network: chain.ArbitrumOne, Asset: usdc,
		TxHash:   "0x" + strings.Repeat("ab", 32),
		EventKey: "log:7", ToAddress: "0x" + strings.Repeat("0", 38) + "AA",
		Units: big.NewInt(1), BlockNumber: 10, BlockTime: time.Unix(1_800_000_000, 0), Finalized: true,
	}
	n, err := svc.normalizeObservation(good)
	require.NoError(t, err)
	require.Equal(t, "0x00000000000000000000000000000000000000aa", n.toAddress)
	require.Equal(t, "0.000001", n.amount.String())

	fake := good
	fake.Asset.Contract = "0x0000000000000000000000000000000000000bad"
	_, err = svc.normalizeObservation(fake)
	require.Error(t, err, "non-whitelisted contract")

	wrongDecimals := good
	wrongDecimals.Asset.Decimals = 18
	_, err = svc.normalizeObservation(wrongDecimals)
	require.Error(t, err)

	zero := good
	zero.Units = big.NewInt(0)
	_, err = svc.normalizeObservation(zero)
	require.Error(t, err)

	badKey := good
	badKey.EventKey = "acct:x"
	_, err = svc.normalizeObservation(badKey)
	require.Error(t, err)

	otherNet := good
	otherNet.Network = chain.ArbitrumSepolia
	_, err = svc.normalizeObservation(otherNet)
	require.Error(t, err)

	sol := chain.MainnetAssets()[2]
	solObs := chain.Observation{
		Network: chain.SolanaMainnet, Asset: sol,
		TxHash:       "5" + "j7s6NiJS3JAkvgkoc18WVAsiSaci2pxB2A6ueCJP4tprA2TFg9wSyTLeYouxPBJEMzJinENTkpA52YStRW5Dia7",
		EventKey:     "acct:7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		TokenAccount: "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		ToAddress:    "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
		Units:        big.NewInt(505000), BlockNumber: 1, Finalized: true,
	}
	n, err = svc.normalizeObservation(solObs)
	require.NoError(t, err)
	require.Equal(t, "0.505", n.amount.String())
	require.False(t, n.blockTime.IsZero(), "missing block time falls back to now")

	wrongProgram := solObs
	wrongProgram.Asset.TokenProgram = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	_, err = svc.normalizeObservation(wrongProgram)
	require.Error(t, err, "Token-2022 program with same mint is not whitelisted")

	mismatchedKey := solObs
	mismatchedKey.EventKey = "acct:9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	_, err = svc.normalizeObservation(mismatchedKey)
	require.Error(t, err)
}

func TestCryptoPayParseSolanaWindowStrict(t *testing.T) {
	w, err := cryptoPayParseSolanaWindow("")
	require.NoError(t, err)
	require.Equal(t, chain.SolanaWindow{}, w)
	w, err = cryptoPayParseSolanaWindow(`{"until":"a","anchor":"b","before":"c"}`)
	require.NoError(t, err)
	require.Equal(t, chain.SolanaWindow{Until: "a", Anchor: "b", Before: "c"}, w)
	for _, bad := range []string{"null", " null ", "plainSig", `{"until":"a"} {}`, `{"until":"a"}x`, `{"until":"a","extra":1}`, `[]`} {
		_, err := cryptoPayParseSolanaWindow(bad)
		require.Error(t, err, bad)
	}
}
