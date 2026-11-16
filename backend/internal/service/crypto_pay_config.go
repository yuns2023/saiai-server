package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// Settings keys for the three independent runtime switches and the limits.
// All switches default to false when absent.
const (
	SettingCryptoPayAllowNewAddresses = "crypto_pay_allow_new_addresses"
	SettingCryptoPayScanningEnabled   = "crypto_pay_scanning_enabled"
	SettingCryptoPayAutoCreditEnabled = "crypto_pay_auto_credit_enabled"
	SettingCryptoPayMinAmount         = "crypto_pay_min_amount"
	SettingCryptoPayMaxAmount         = "crypto_pay_max_amount"
	SettingCryptoPayOrderTTLMinutes   = "crypto_pay_order_ttl_minutes"
	SettingCryptoPayHoldThreshold     = "crypto_pay_hold_threshold_amount"
)

var cryptoPaySettingKeys = []string{
	SettingCryptoPayAllowNewAddresses, SettingCryptoPayScanningEnabled, SettingCryptoPayAutoCreditEnabled,
	SettingCryptoPayMinAmount, SettingCryptoPayMaxAmount, SettingCryptoPayOrderTTLMinutes, SettingCryptoPayHoldThreshold,
}

var (
	cryptoPayDefaultMinAmount     = decimal.NewFromInt(1)
	cryptoPayDefaultMaxAmount     = decimal.NewFromInt(10000)
	cryptoPayDefaultHoldThreshold = decimal.NewFromInt(5000)
	// users.balance is decimal(20,8): amounts must stay below 10^12.
	cryptoPayAmountCeiling = decimal.New(1, 12)
)

const (
	cryptoPayDefaultOrderTTLMinutes = 180
	cryptoPayMaxOrderTTLMinutes     = 7 * 24 * 60
	// Balance scale is 8 decimal places; assets with more decimals could not
	// be credited exactly, so they are rejected at configuration time.
	cryptoPayMaxAssetDecimals = 8
)

// Error reasons exposed to clients.
const (
	CryptoPayReasonDisabled           = "CRYPTO_PAY_DISABLED"
	CryptoPayReasonAddressUnavailable = "CRYPTO_PAY_ADDRESS_UNAVAILABLE"
)

// CryptoPayEVMDeriver derives the lowercase 0x address at m/44'/60'/0'/0/index
// from an account-level xpub (chain.EVMAddressFromXpub).
type CryptoPayEVMDeriver func(xpub string, index uint32) (string, error)

// CryptoPaySolanaManifestVerifier verifies an offline-signed Solana pool
// manifest against the trusted signer (chain.VerifySolanaPoolManifest).
type CryptoPaySolanaManifestVerifier func(raw []byte, trustedSigner string) (*chain.SolanaPoolManifest, error)

// CryptoPaySolanaATAFunc computes an associated token account (chain.SolanaATA).
type CryptoPaySolanaATAFunc func(owner, mint, program string) (string, error)

// CryptoPayChainDeps carries the chain package implementations. Any nil
// member disables only the features that need it.
type CryptoPayChainDeps struct {
	EVMScanner           chain.EVMScanner
	SolanaScanner        chain.SolanaScanner
	DeriveEVMAddress     CryptoPayEVMDeriver
	VerifySolanaManifest CryptoPaySolanaManifestVerifier
	SolanaATA            CryptoPaySolanaATAFunc
}

// cryptoPayNetwork is one configured, validated network.
type cryptoPayNetwork struct {
	Network             chain.Network
	Family              chain.Family
	Label               string
	Assets              []chain.Asset
	FinalityHintMinutes int
	ExplorerTxURL       string
	ExplorerAddressURL  string
}

func (n *cryptoPayNetwork) asset(symbol string) (chain.Asset, bool) {
	for _, a := range n.Assets {
		if strings.EqualFold(a.Symbol, symbol) {
			return a, true
		}
	}
	return chain.Asset{}, false
}

// assetByContract matches the identity (contract/mint and token program).
func (n *cryptoPayNetwork) assetByContract(contract, program string) (chain.Asset, bool) {
	for _, a := range n.Assets {
		if n.Family == chain.FamilyEVM {
			if strings.EqualFold(a.Contract, contract) {
				return a, true
			}
			continue
		}
		if a.Contract == contract && a.TokenProgram == program {
			return a, true
		}
	}
	return chain.Asset{}, false
}

var (
	cryptoPayEVMAddressRe = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	cryptoPayBase58Re     = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
	cryptoPayEVMTxHashRe  = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	cryptoPaySolSigRe     = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{64,90}$`)
)

// parseCryptoPayNetworks turns the deployment config into validated networks.
// Problems are returned as warnings and disable only the affected network.
func parseCryptoPayNetworks(cfg config.CryptoPayConfig) ([]*cryptoPayNetwork, []string) {
	var networks []*cryptoPayNetwork
	var warnings []string
	if !cfg.Enabled {
		return nil, nil
	}
	if raw := strings.TrimSpace(cfg.EVMNetwork); raw != "" {
		n, err := buildCryptoPayEVMNetwork(chain.Network(raw), strings.TrimSpace(cfg.EVMAssets))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("crypto_pay EVM network %q disabled: %v", raw, err))
		} else {
			networks = append(networks, n)
		}
	}
	if raw := strings.TrimSpace(cfg.SolanaNetwork); raw != "" {
		n, err := buildCryptoPaySolanaNetwork(chain.Network(raw), strings.TrimSpace(cfg.SolanaAssets))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("crypto_pay Solana network %q disabled: %v", raw, err))
		} else {
			networks = append(networks, n)
		}
	}
	return networks, warnings
}

func buildCryptoPayEVMNetwork(network chain.Network, override string) (*cryptoPayNetwork, error) {
	n := &cryptoPayNetwork{Network: network, Family: chain.FamilyEVM, FinalityHintMinutes: 20}
	switch network {
	case chain.ArbitrumOne:
		if override != "" {
			return nil, fmt.Errorf("asset override is not allowed on mainnet")
		}
		n.Label = "Arbitrum"
		n.ExplorerTxURL = "https://arbiscan.io/tx/{tx}"
		n.ExplorerAddressURL = "https://arbiscan.io/address/{addr}"
		n.Assets = mainnetAssetsFor(network)
		return n, nil
	case chain.ArbitrumSepolia:
		n.Label = "Arbitrum Sepolia"
		n.ExplorerTxURL = "https://sepolia.arbiscan.io/tx/{tx}"
		n.ExplorerAddressURL = "https://sepolia.arbiscan.io/address/{addr}"
	default:
		return nil, fmt.Errorf("unsupported EVM network")
	}
	if override == "" {
		return nil, fmt.Errorf("testnet requires crypto_pay.evm_assets")
	}
	assets, err := parseCryptoPayAssetList(network, override, false)
	if err != nil {
		return nil, err
	}
	n.Assets = assets
	return n, nil
}

func buildCryptoPaySolanaNetwork(network chain.Network, override string) (*cryptoPayNetwork, error) {
	n := &cryptoPayNetwork{Network: network, Family: chain.FamilySolana, FinalityHintMinutes: 1}
	switch network {
	case chain.SolanaMainnet:
		if override != "" {
			return nil, fmt.Errorf("asset override is not allowed on mainnet")
		}
		n.Label = "Solana"
		n.ExplorerTxURL = "https://solscan.io/tx/{tx}"
		n.ExplorerAddressURL = "https://solscan.io/account/{addr}"
		n.Assets = mainnetAssetsFor(network)
		return n, nil
	case chain.SolanaDevnet:
		n.Label = "Solana Devnet"
		n.ExplorerTxURL = "https://solscan.io/tx/{tx}?cluster=devnet"
		n.ExplorerAddressURL = "https://solscan.io/account/{addr}?cluster=devnet"
	default:
		return nil, fmt.Errorf("unsupported Solana network")
	}
	if override == "" {
		return nil, fmt.Errorf("testnet requires crypto_pay.solana_assets")
	}
	assets, err := parseCryptoPayAssetList(network, override, true)
	if err != nil {
		return nil, err
	}
	n.Assets = assets
	return n, nil
}

func mainnetAssetsFor(network chain.Network) []chain.Asset {
	var out []chain.Asset
	for _, a := range chain.MainnetAssets() {
		if a.Network == network {
			out = append(out, a)
		}
	}
	return out
}

// parseCryptoPayAssetList parses "SYM:contract:decimals" (EVM) or
// "SYM:mint:program:decimals" (Solana), comma separated.
func parseCryptoPayAssetList(network chain.Network, raw string, solana bool) ([]chain.Asset, error) {
	var out []chain.Asset
	seen := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		parts := strings.Split(strings.TrimSpace(item), ":")
		want := 3
		if solana {
			want = 4
		}
		if len(parts) != want {
			return nil, fmt.Errorf("invalid asset entry %q", item)
		}
		symbol := strings.ToUpper(strings.TrimSpace(parts[0]))
		if symbol != "USDC" && symbol != "USDT" {
			return nil, fmt.Errorf("unsupported asset symbol %q", symbol)
		}
		if seen[symbol] {
			return nil, fmt.Errorf("duplicate asset symbol %q", symbol)
		}
		seen[symbol] = true
		decimals, err := strconv.Atoi(strings.TrimSpace(parts[want-1]))
		if err != nil || decimals < 0 || decimals > cryptoPayMaxAssetDecimals {
			return nil, fmt.Errorf("invalid decimals for %s", symbol)
		}
		asset := chain.Asset{Network: network, Symbol: symbol, Decimals: int32(decimals)}
		if solana {
			asset.Contract = strings.TrimSpace(parts[1])
			asset.TokenProgram = strings.TrimSpace(parts[2])
			if !cryptoPayBase58Re.MatchString(asset.Contract) || !cryptoPayBase58Re.MatchString(asset.TokenProgram) {
				return nil, fmt.Errorf("invalid mint or token program for %s", symbol)
			}
		} else {
			asset.Contract = strings.ToLower(strings.TrimSpace(parts[1]))
			if !cryptoPayEVMAddressRe.MatchString(asset.Contract) {
				return nil, fmt.Errorf("invalid contract for %s", symbol)
			}
		}
		out = append(out, asset)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no assets configured")
	}
	return out, nil
}

// CryptoPaySettings is the runtime configuration stored in the settings table.
type CryptoPaySettings struct {
	AllowNewAddresses   bool
	ScanningEnabled     bool
	AutoCreditEnabled   bool
	MinAmount           decimal.Decimal
	MaxAmount           decimal.Decimal
	OrderTTLMinutes     int
	HoldThresholdAmount decimal.Decimal
}

// CryptoPayAdminSettings is the admin settings DTO (money as strings).
type CryptoPayAdminSettings struct {
	AllowNewAddresses   bool   `json:"allow_new_addresses"`
	ScanningEnabled     bool   `json:"scanning_enabled"`
	AutoCreditEnabled   bool   `json:"auto_credit_enabled"`
	MinAmount           string `json:"min_amount"`
	MaxAmount           string `json:"max_amount"`
	OrderTTLMinutes     int    `json:"order_ttl_minutes"`
	HoldThresholdAmount string `json:"hold_threshold_amount"`
	// Read-only deployment status.
	FeatureConfigured bool     `json:"feature_configured"`
	Networks          []string `json:"networks"`
	EVMScannerReady   bool     `json:"evm_scanner_ready"`
	SolanaScannerReady bool    `json:"solana_scanner_ready"`
}

// UpdateCryptoPaySettingsRequest is a partial update; nil fields are kept.
type UpdateCryptoPaySettingsRequest struct {
	AllowNewAddresses   *bool   `json:"allow_new_addresses"`
	ScanningEnabled     *bool   `json:"scanning_enabled"`
	AutoCreditEnabled   *bool   `json:"auto_credit_enabled"`
	MinAmount           *string `json:"min_amount"`
	MaxAmount           *string `json:"max_amount"`
	OrderTTLMinutes     *int    `json:"order_ttl_minutes"`
	HoldThresholdAmount *string `json:"hold_threshold_amount"`
}

func parseCryptoPayDecimal(raw string, fallback decimal.Decimal) decimal.Decimal {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !value.IsPositive() || value.GreaterThanOrEqual(cryptoPayAmountCeiling) || !value.Equal(value.Truncate(cryptoPayMaxAssetDecimals)) {
		return fallback
	}
	return value
}

// GetSettings reads the runtime settings; missing or invalid values fall back
// to defaults and every switch defaults to off.
func (s *CryptoPayService) GetSettings(ctx context.Context) (*CryptoPaySettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, cryptoPaySettingKeys)
	if err != nil {
		return nil, fmt.Errorf("get crypto pay settings: %w", err)
	}
	st := &CryptoPaySettings{
		AllowNewAddresses:   values[SettingCryptoPayAllowNewAddresses] == "true",
		ScanningEnabled:     values[SettingCryptoPayScanningEnabled] == "true",
		AutoCreditEnabled:   values[SettingCryptoPayAutoCreditEnabled] == "true",
		MinAmount:           parseCryptoPayDecimal(values[SettingCryptoPayMinAmount], cryptoPayDefaultMinAmount),
		MaxAmount:           parseCryptoPayDecimal(values[SettingCryptoPayMaxAmount], cryptoPayDefaultMaxAmount),
		OrderTTLMinutes:     parsePositiveInt(values[SettingCryptoPayOrderTTLMinutes], cryptoPayDefaultOrderTTLMinutes),
		HoldThresholdAmount: parseCryptoPayDecimal(values[SettingCryptoPayHoldThreshold], cryptoPayDefaultHoldThreshold),
	}
	if st.OrderTTLMinutes > cryptoPayMaxOrderTTLMinutes {
		st.OrderTTLMinutes = cryptoPayDefaultOrderTTLMinutes
	}
	if st.MaxAmount.LessThan(st.MinAmount) {
		st.MinAmount, st.MaxAmount = cryptoPayDefaultMinAmount, cryptoPayDefaultMaxAmount
	}
	return st, nil
}

// GetAdminSettings returns the settings DTO plus read-only deployment status.
func (s *CryptoPayService) GetAdminSettings(ctx context.Context) (*CryptoPayAdminSettings, error) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s.networks))
	for _, n := range s.networks {
		names = append(names, string(n.Network))
	}
	return &CryptoPayAdminSettings{
		AllowNewAddresses: st.AllowNewAddresses, ScanningEnabled: st.ScanningEnabled, AutoCreditEnabled: st.AutoCreditEnabled,
		MinAmount: st.MinAmount.String(), MaxAmount: st.MaxAmount.String(), OrderTTLMinutes: st.OrderTTLMinutes,
		HoldThresholdAmount: st.HoldThresholdAmount.String(),
		FeatureConfigured:   len(s.networks) > 0, Networks: names,
		EVMScannerReady: s.evmScanner() != nil, SolanaScannerReady: s.solanaScanner() != nil,
	}, nil
}

// UpdateSettings validates and stores a partial update, then audits it.
func (s *CryptoPayService) UpdateSettings(ctx context.Context, req UpdateCryptoPaySettingsRequest, operator string) (*CryptoPayAdminSettings, error) {
	current, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	next := *current
	updates := map[string]string{}
	parseAmount := func(raw *string, key string, dst *decimal.Decimal) error {
		if raw == nil {
			return nil
		}
		value, err := decimal.NewFromString(strings.TrimSpace(*raw))
		if err != nil || !value.IsPositive() || value.GreaterThanOrEqual(cryptoPayAmountCeiling) || !value.Equal(value.Truncate(cryptoPayMaxAssetDecimals)) {
			return infraerrors.BadRequest("CRYPTO_PAY_SETTINGS_INVALID", "invalid amount for "+key)
		}
		*dst = value
		updates[key] = value.String()
		return nil
	}
	if err := parseAmount(req.MinAmount, SettingCryptoPayMinAmount, &next.MinAmount); err != nil {
		return nil, err
	}
	if err := parseAmount(req.MaxAmount, SettingCryptoPayMaxAmount, &next.MaxAmount); err != nil {
		return nil, err
	}
	if err := parseAmount(req.HoldThresholdAmount, SettingCryptoPayHoldThreshold, &next.HoldThresholdAmount); err != nil {
		return nil, err
	}
	if req.OrderTTLMinutes != nil {
		if *req.OrderTTLMinutes <= 0 || *req.OrderTTLMinutes > cryptoPayMaxOrderTTLMinutes {
			return nil, infraerrors.BadRequest("CRYPTO_PAY_SETTINGS_INVALID", "invalid order_ttl_minutes")
		}
		next.OrderTTLMinutes = *req.OrderTTLMinutes
		updates[SettingCryptoPayOrderTTLMinutes] = strconv.Itoa(*req.OrderTTLMinutes)
	}
	setBool := func(v *bool, key string, dst *bool) {
		if v != nil {
			*dst = *v
			updates[key] = strconv.FormatBool(*v)
		}
	}
	setBool(req.AllowNewAddresses, SettingCryptoPayAllowNewAddresses, &next.AllowNewAddresses)
	setBool(req.ScanningEnabled, SettingCryptoPayScanningEnabled, &next.ScanningEnabled)
	setBool(req.AutoCreditEnabled, SettingCryptoPayAutoCreditEnabled, &next.AutoCreditEnabled)
	if next.MaxAmount.LessThan(next.MinAmount) {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_SETTINGS_INVALID", "max_amount is less than min_amount")
	}
	if len(updates) > 0 {
		if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
			return nil, fmt.Errorf("save crypto pay settings: %w", err)
		}
		if err := s.writeAuditDirect(ctx, "SETTINGS_UPDATED", nil, nil, nil, operator, updates); err != nil {
			// Settings are already stored; do not fail the request (a retry
			// would be a no-op anyway), but make the gap visible.
			cryptoPayLogf("audit settings update failed: %v", err)
		}
	}
	return s.GetAdminSettings(ctx)
}
