package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
)

// Deposit statuses stored in crypto_pay_deposits.status.
const (
	cryptoDepositObserved  = "observed"
	cryptoDepositFinalized = "finalized"
	cryptoDepositCredited  = "credited"
	cryptoDepositHeld      = "held"
	cryptoDepositOrphaned  = "orphaned"
)

// Order statuses (computed, never stored).
const (
	CryptoPayOrderPending = "pending"
	CryptoPayOrderPaid    = "paid"
	CryptoPayOrderExpired = "expired"
)

const (
	cryptoPayMaxPendingOrders = 10
	// Orders are suggestions in dollars: at most 2 decimals, rendered "25.50".
	cryptoPayOrderAmountDecimals = 2
	cryptoPayOrderDepositCap     = 1000 // safety bound on Order.deposits; far above one order window
	cryptoPayOperatorSystem      = "system"
)

type cryptoPayBalanceCache interface {
	InvalidateUserBalance(ctx context.Context, userID int64) error
}

// CryptoPayService owns the stablecoin top-up ledger: address allocation,
// deposit ingestion, crediting and the read APIs. Chain access is injected
// (CryptoPayChainDeps) and RPC calls are never made inside a DB transaction.
type CryptoPayService struct {
	db               *sql.DB
	settingRepo      SettingRepository
	networks         []*cryptoPayNetwork
	byNetwork        map[chain.Network]*cryptoPayNetwork
	deps             CryptoPayChainDeps
	signerPubkey     string
	evmScanFromBlock uint64
	scanInterval     time.Duration
	balanceCache     cryptoPayBalanceCache
	authCache        APIKeyAuthCacheInvalidator
	now              func() time.Time
}

// NewCryptoPayService builds the service. A disabled or invalid configuration
// yields a service with no networks: every user endpoint reports disabled.
func NewCryptoPayService(db *sql.DB, settingRepo SettingRepository, cfg config.CryptoPayConfig, deps CryptoPayChainDeps,
	balanceCache cryptoPayBalanceCache, authCache APIKeyAuthCacheInvalidator) *CryptoPayService {
	networks, warnings := parseCryptoPayNetworks(cfg)
	for _, w := range warnings {
		cryptoPayLogf("%s", w)
	}
	s := &CryptoPayService{
		db: db, settingRepo: settingRepo, networks: networks, byNetwork: map[chain.Network]*cryptoPayNetwork{},
		signerPubkey: strings.TrimSpace(cfg.SolanaPoolSignerPubkey), evmScanFromBlock: cfg.EVMScanFromBlock,
		balanceCache: balanceCache, authCache: authCache, now: time.Now,
	}
	interval := cfg.ScanIntervalSeconds
	if interval <= 0 {
		interval = 30
	}
	s.scanInterval = time.Duration(interval) * time.Second
	for _, n := range networks {
		s.byNetwork[n.Network] = n
	}
	// A scanner for a network that is not configured is ignored.
	if deps.EVMScanner != nil {
		if n := s.byNetwork[deps.EVMScanner.Network()]; n == nil || n.Family != chain.FamilyEVM {
			cryptoPayLogf("EVM scanner for %q ignored: network not configured", deps.EVMScanner.Network())
			deps.EVMScanner = nil
		}
	}
	if deps.SolanaScanner != nil {
		if n := s.byNetwork[deps.SolanaScanner.Network()]; n == nil || n.Family != chain.FamilySolana {
			cryptoPayLogf("Solana scanner for %q ignored: network not configured", deps.SolanaScanner.Network())
			deps.SolanaScanner = nil
		}
	}
	s.deps = deps
	return s
}

func cryptoPayLogf(format string, args ...any) {
	logger.LegacyPrintf("service.crypto_pay", format, args...)
}

func (s *CryptoPayService) evmScanner() chain.EVMScanner       { return s.deps.EVMScanner }
func (s *CryptoPayService) solanaScanner() chain.SolanaScanner { return s.deps.SolanaScanner }

func (s *CryptoPayService) network(name string) (*cryptoPayNetwork, bool) {
	n, ok := s.byNetwork[chain.Network(strings.TrimSpace(name))]
	return n, ok
}

// Enabled reports whether the deployment has at least one usable network.
func (s *CryptoPayService) Enabled() bool { return s != nil && len(s.networks) > 0 }

// ---- DTOs (field names are the HTTP contract) ----

type CryptoPayAssetInfo struct {
	Symbol   string `json:"symbol"`
	Contract string `json:"contract"`
}

type CryptoPayNetworkInfo struct {
	Network             string               `json:"network"`
	Label               string               `json:"label"`
	Assets              []CryptoPayAssetInfo `json:"assets"`
	FinalityHintMinutes int                  `json:"finality_hint_minutes"`
	ExplorerTxURL       string               `json:"explorer_tx_url"`
	ExplorerAddressURL  string               `json:"explorer_address_url"`
}

type CryptoPayUserConfig struct {
	Enabled         bool                   `json:"enabled"`
	MinAmount       string                 `json:"min_amount"`
	MaxAmount       string                 `json:"max_amount"`
	OrderTTLMinutes int                    `json:"order_ttl_minutes"`
	Networks        []CryptoPayNetworkInfo `json:"networks"`
}

type CryptoPayDeposit struct {
	ID         int64      `json:"id"`
	Network    string     `json:"network"`
	Asset      string     `json:"asset"`
	Amount     string     `json:"amount"`
	TxHash     string     `json:"tx_hash"`
	Status     string     `json:"status"`
	BlockTime  time.Time  `json:"block_time"`
	CreditedAt *time.Time `json:"credited_at"`
}

type CryptoPayOrder struct {
	ID               int64              `json:"id"`
	Network          string             `json:"network"`
	Asset            string             `json:"asset"`
	Amount           string             `json:"amount"`
	Address          string             `json:"address"`
	Status           string             `json:"status"`
	CreatedAt        time.Time          `json:"created_at"`
	ExpiresAt        time.Time          `json:"expires_at"`
	ReceivedAmount   string             `json:"received_amount"`
	ConfirmingAmount string             `json:"confirming_amount"`
	CreditedAmount   string             `json:"credited_amount"`
	Deposits         []CryptoPayDeposit `json:"deposits"`
}

type CryptoPayUserAddress struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

// cryptoPayPublicDepositStatus maps the ledger status to the API status:
// observed and finalized-but-not-yet-credited are both "confirming".
func cryptoPayPublicDepositStatus(status string) string {
	switch status {
	case cryptoDepositCredited, cryptoDepositHeld, cryptoDepositOrphaned:
		return status
	default:
		return "confirming"
	}
}

// computeCryptoPayOrderStatus: finalized received >= amount → paid; otherwise
// pending until expires_at, then expired.
func computeCryptoPayOrderStatus(amount, finalizedReceived decimal.Decimal, expiresAt, now time.Time) string {
	if finalizedReceived.GreaterThanOrEqual(amount) {
		return CryptoPayOrderPaid
	}
	if now.Before(expiresAt) {
		return CryptoPayOrderPending
	}
	return CryptoPayOrderExpired
}

// formatCryptoAmount renders an exact decimal without trailing zeros.
func formatCryptoAmount(d decimal.Decimal) string {
	if d.IsZero() {
		return "0"
	}
	return d.String()
}

// ---- user API ----

// GetUserConfig returns the public configuration. Disabled → networks = [].
func (s *CryptoPayService) GetUserConfig(ctx context.Context) (*CryptoPayUserConfig, error) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := &CryptoPayUserConfig{
		Enabled: s.Enabled() && st.AllowNewAddresses, MinAmount: st.MinAmount.String(), MaxAmount: st.MaxAmount.String(),
		OrderTTLMinutes: st.OrderTTLMinutes, Networks: []CryptoPayNetworkInfo{},
	}
	if !out.Enabled {
		return out, nil
	}
	for _, n := range s.networks {
		info := CryptoPayNetworkInfo{
			Network: string(n.Network), Label: n.Label, Assets: []CryptoPayAssetInfo{},
			FinalityHintMinutes: n.FinalityHintMinutes, ExplorerTxURL: n.ExplorerTxURL, ExplorerAddressURL: n.ExplorerAddressURL,
		}
		for _, a := range n.Assets {
			info.Assets = append(info.Assets, CryptoPayAssetInfo{Symbol: a.Symbol, Contract: a.Contract})
		}
		out.Networks = append(out.Networks, info)
	}
	return out, nil
}

// CreateOrder allocates (or reuses) the user's fixed address on the network
// and records a suggested amount with a countdown.
func (s *CryptoPayService) CreateOrder(ctx context.Context, userID int64, networkName, assetSymbol, amountRaw string) (*CryptoPayOrder, error) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !s.Enabled() || !st.AllowNewAddresses {
		return nil, infraerrors.ServiceUnavailable(CryptoPayReasonDisabled, "crypto top-up is disabled")
	}
	n, ok := s.network(networkName)
	if !ok {
		return nil, infraerrors.BadRequest(CryptoPayReasonNetworkUnsupported, "unsupported network")
	}
	asset, ok := n.asset(strings.TrimSpace(assetSymbol))
	if !ok {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_ASSET_UNSUPPORTED", "unsupported asset")
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(amountRaw))
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Truncate(cryptoPayOrderAmountDecimals)) ||
		amount.LessThan(st.MinAmount) || amount.GreaterThan(st.MaxAmount) {
		return nil, infraerrors.BadRequest(CryptoPayReasonAmountInvalid,
			fmt.Sprintf("amount must be between %s and %s with at most %d decimals", st.MinAmount, st.MaxAmount, cryptoPayOrderAmountDecimals))
	}
	if err := s.requireActiveUser(ctx, userID); err != nil {
		return nil, err
	}
	now := s.now()
	var pending int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crypto_pay_orders WHERE user_id = $1 AND expires_at > $2`, userID, now).Scan(&pending); err != nil {
		return nil, fmt.Errorf("count crypto pay orders: %w", err)
	}
	if pending >= cryptoPayMaxPendingOrders {
		return nil, infraerrors.TooManyRequests("CRYPTO_PAY_TOO_MANY_ORDERS", "too many open crypto top-up orders")
	}
	addr, err := s.EnsureAddress(ctx, userID, n.Network)
	if err != nil {
		return nil, err
	}
	var orderID int64
	expiresAt := now.Add(time.Duration(st.OrderTTLMinutes) * time.Minute)
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_orders (user_id, address_id, network, asset, amount, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7) RETURNING id`,
		userID, addr.ID, string(n.Network), asset.Symbol, amount.String(), now, expiresAt).Scan(&orderID); err != nil {
		return nil, fmt.Errorf("create crypto pay order: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE crypto_pay_addresses SET last_order_at = $2 WHERE id = $1`, addr.ID, now); err != nil {
		cryptoPayLogf("touch address %d failed: %v", addr.ID, err)
	}
	return s.GetUserOrder(ctx, userID, orderID)
}

func (s *CryptoPayService) requireActiveUser(ctx context.Context, userID int64) error {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL`, userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("get crypto pay user: %w", err)
	}
	if status != StatusActive {
		return infraerrors.Forbidden("USER_INACTIVE", "user account is inactive")
	}
	return nil
}

type cryptoPayOrderRow struct {
	id        int64
	network   string
	asset     string
	amount    decimal.Decimal
	addressID int64
	address   string
	createdAt time.Time
	expiresAt time.Time
}

const cryptoPayOrderSelect = `
	SELECT o.id, o.network, o.asset, o.amount, o.address_id, a.address, o.created_at, o.expires_at
	FROM crypto_pay_orders o JOIN crypto_pay_addresses a ON a.id = o.address_id`

func scanCryptoPayOrder(scan func(dest ...any) error) (*cryptoPayOrderRow, error) {
	var r cryptoPayOrderRow
	if err := scan(&r.id, &r.network, &r.asset, &r.amount, &r.addressID, &r.address, &r.createdAt, &r.expiresAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetUserOrder returns the caller's order; another user's order is 404.
func (s *CryptoPayService) GetUserOrder(ctx context.Context, userID, orderID int64) (*CryptoPayOrder, error) {
	row, err := scanCryptoPayOrder(s.db.QueryRowContext(ctx, cryptoPayOrderSelect+` WHERE o.id = $1 AND o.user_id = $2`, orderID, userID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.NotFound("CRYPTO_PAY_ORDER_NOT_FOUND", "order not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get crypto pay order: %w", err)
	}
	return s.buildOrder(ctx, row)
}

// ListUserOrders returns the caller's orders, newest first.
func (s *CryptoPayService) ListUserOrders(ctx context.Context, userID int64, page, pageSize int) ([]CryptoPayOrder, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crypto_pay_orders WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count crypto pay orders: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, cryptoPayOrderSelect+` WHERE o.user_id = $1 ORDER BY o.created_at DESC, o.id DESC LIMIT $2 OFFSET $3`,
		userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list crypto pay orders: %w", err)
	}
	var orderRows []*cryptoPayOrderRow
	for rows.Next() {
		r, scanErr := scanCryptoPayOrder(rows.Scan)
		if scanErr != nil {
			_ = rows.Close()
			return nil, 0, scanErr
		}
		orderRows = append(orderRows, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, 0, err
	}
	out := make([]CryptoPayOrder, 0, len(orderRows))
	for _, r := range orderRows {
		o, buildErr := s.buildOrder(ctx, r)
		if buildErr != nil {
			return nil, 0, buildErr
		}
		out = append(out, *o)
	}
	return out, total, nil
}

// buildOrder computes amounts and status from inflows to the order's address
// with block_time >= created_at (all assets on that network).
func (s *CryptoPayService) buildOrder(ctx context.Context, r *cryptoPayOrderRow) (*CryptoPayOrder, error) {
	// received = finalized inflows only (finalized/credited/held); confirming =
	// observed, not yet finalized. They never overlap.
	var received, confirming, credited decimal.Decimal
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount) FILTER (WHERE status IN ('finalized','credited','held')), 0),
		       COALESCE(SUM(amount) FILTER (WHERE status = 'observed'), 0),
		       COALESCE(SUM(amount) FILTER (WHERE status = 'credited'), 0)
		FROM crypto_pay_deposits WHERE address_id = $1 AND block_time >= $2`,
		r.addressID, r.createdAt).Scan(&received, &confirming, &credited); err != nil {
		return nil, fmt.Errorf("sum crypto pay deposits: %w", err)
	}
	deposits, err := s.queryDeposits(ctx, `WHERE address_id = $1 AND block_time >= $2 ORDER BY block_time DESC, id DESC LIMIT $3`,
		r.addressID, r.createdAt, cryptoPayOrderDepositCap)
	if err != nil {
		return nil, err
	}
	return &CryptoPayOrder{
		ID: r.id, Network: r.network, Asset: r.asset, Amount: r.amount.StringFixed(cryptoPayOrderAmountDecimals), Address: r.address,
		Status:    computeCryptoPayOrderStatus(r.amount, received, r.expiresAt, s.now()),
		CreatedAt: r.createdAt, ExpiresAt: r.expiresAt,
		ReceivedAmount: formatCryptoAmount(received), ConfirmingAmount: formatCryptoAmount(confirming),
		CreditedAmount: formatCryptoAmount(credited), Deposits: deposits,
	}, nil
}

func (s *CryptoPayService) queryDeposits(ctx context.Context, where string, args ...any) ([]CryptoPayDeposit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, network, asset_symbol, amount, tx_hash, status, block_time, credited_at
		FROM crypto_pay_deposits `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query crypto pay deposits: %w", err)
	}
	out := []CryptoPayDeposit{}
	for rows.Next() {
		var d CryptoPayDeposit
		var amount decimal.Decimal
		var creditedAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.Network, &d.Asset, &amount, &d.TxHash, &d.Status, &d.BlockTime, &creditedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.Amount = formatCryptoAmount(amount)
		d.Status = cryptoPayPublicDepositStatus(d.Status)
		if creditedAt.Valid {
			t := creditedAt.Time
			d.CreditedAt = &t
		}
		out = append(out, d)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, nil
}

// ListUserDeposits returns all of the caller's inflows (inside and outside orders).
func (s *CryptoPayService) ListUserDeposits(ctx context.Context, userID int64, page, pageSize int) ([]CryptoPayDeposit, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crypto_pay_deposits WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count crypto pay deposits: %w", err)
	}
	items, err := s.queryDeposits(ctx, `WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
		userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListUserAddresses returns the caller's allocated addresses.
func (s *CryptoPayService) ListUserAddresses(ctx context.Context, userID int64) ([]CryptoPayUserAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT network, address FROM crypto_pay_addresses WHERE user_id = $1 ORDER BY network`, userID)
	if err != nil {
		return nil, fmt.Errorf("list crypto pay addresses: %w", err)
	}
	out := []CryptoPayUserAddress{}
	for rows.Next() {
		var a CryptoPayUserAddress
		if err := rows.Scan(&a.Network, &a.Address); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- audit ----

type cryptoPayExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func writeCryptoPayAudit(ctx context.Context, q cryptoPayExecer, action string, depositID, userID, keysetID *int64, operator string, detail any) error {
	text := ""
	if detail != nil {
		encoded, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode audit detail: %w", err)
		}
		text = string(encoded)
	}
	if operator == "" {
		operator = cryptoPayOperatorSystem
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO crypto_pay_audit_logs (action, deposit_id, user_id, keyset_id, operator, detail)
		VALUES ($1, $2, $3, $4, $5, $6)`, action, depositID, userID, keysetID, operator, text)
	if err != nil {
		return fmt.Errorf("write crypto pay audit: %w", err)
	}
	return nil
}

func (s *CryptoPayService) writeAuditDirect(ctx context.Context, action string, depositID, userID, keysetID *int64, operator string, detail any) error {
	return writeCryptoPayAudit(ctx, s.db, action, depositID, userID, keysetID, operator, detail)
}
