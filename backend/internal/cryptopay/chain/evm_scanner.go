package chain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFinalized is returned when finalized data was requested for a block
// or transaction above the current finalized head.
var ErrNotFinalized = errors.New("chain: not finalized yet")

// ErrTxNotFound is returned when the node does not know a transaction.
var ErrTxNotFound = errors.New("chain: transaction not found")

// EVMScannerConfig configures an EVM JSON-RPC scanner.
type EVMScannerConfig struct {
	Network Network
	RPC     RPCOptions
	// Assets is the token whitelist. nil = MainnetAssets() rows of Network.
	// Required (non-nil) for networks without mainnet assets (e.g. sepolia).
	Assets []Asset
	// MaxBlockRange caps one eth_getLogs block span before any provider error
	// (default 2000). Ranges are further bisected on provider limits.
	MaxBlockRange uint64
	// MaxAddressesPerQuery caps the topic2 OR-list (default 200).
	MaxAddressesPerQuery int
}

// EVMRPCScanner implements EVMScanner over plain JSON-RPC.
type EVMRPCScanner struct {
	network      Network
	rpc          *rpcClient
	assets       map[string]Asset // lowercase contract -> asset
	contracts    []string         // sorted lowercase contracts
	maxRange     uint64
	maxAddresses int

	mu         sync.Mutex
	blockCache map[uint64]blockHeader
}

type blockHeader struct {
	Hash string
	Time time.Time
	// Finalized is true when the header was fetched at or below a known
	// finalized head; only such entries may back a Finalized observation.
	Finalized bool
}

const evmBlockCacheSize = 4096

var _ EVMScanner = (*EVMRPCScanner)(nil)

// NewEVMScanner builds an EVM scanner.
func NewEVMScanner(cfg EVMScannerConfig) (*EVMRPCScanner, error) {
	if cfg.Network.Family() != FamilyEVM {
		return nil, fmt.Errorf("chain: %q is not an EVM network", cfg.Network)
	}
	assets := cfg.Assets
	if assets == nil {
		assets = assetsForNetwork(MainnetAssets(), cfg.Network)
	}
	s := &EVMRPCScanner{
		network:      cfg.Network,
		assets:       map[string]Asset{},
		maxRange:     cfg.MaxBlockRange,
		maxAddresses: cfg.MaxAddressesPerQuery,
		blockCache:   map[uint64]blockHeader{},
	}
	if s.maxRange == 0 {
		s.maxRange = 2000
	}
	if s.maxAddresses <= 0 {
		s.maxAddresses = 200
	}
	for _, a := range assets {
		if a.Network != cfg.Network {
			return nil, fmt.Errorf("chain: asset %s is for %s, scanner is %s", a.Symbol, a.Network, cfg.Network)
		}
		c, err := NormalizeEVMAddress(a.Contract)
		if err != nil {
			return nil, err
		}
		if c != a.Contract {
			return nil, fmt.Errorf("chain: asset contract %s must be lowercase", a.Contract)
		}
		if _, dup := s.assets[c]; dup {
			return nil, fmt.Errorf("chain: duplicate asset contract %s", c)
		}
		s.assets[c] = a
		s.contracts = append(s.contracts, c)
	}
	if len(s.contracts) == 0 {
		return nil, fmt.Errorf("chain: no whitelisted assets for %s", cfg.Network)
	}
	sort.Strings(s.contracts)
	rpc, err := newRPCClient(cfg.RPC)
	if err != nil {
		return nil, err
	}
	s.rpc = rpc
	return s, nil
}

func assetsForNetwork(all []Asset, n Network) []Asset {
	var out []Asset
	for _, a := range all {
		if a.Network == n {
			out = append(out, a)
		}
	}
	return out
}

// Network implements EVMScanner.
func (s *EVMRPCScanner) Network() Network { return s.network }

// LatestBlock implements EVMScanner (eth_blockNumber).
func (s *EVMRPCScanner) LatestBlock(ctx context.Context) (uint64, error) {
	raw, err := s.rpc.call(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	var h string
	if err := json.Unmarshal(raw, &h); err != nil {
		return 0, fmt.Errorf("chain: eth_blockNumber: %w", err)
	}
	return parseHexUint64(h)
}

type rpcBlock struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

// FinalizedBlock implements EVMScanner (eth_getBlockByNumber("finalized")).
func (s *EVMRPCScanner) FinalizedBlock(ctx context.Context) (uint64, error) {
	b, err := s.getBlock(ctx, "finalized")
	if err != nil {
		return 0, err
	}
	n, err := parseHexUint64(b.Number)
	if err != nil {
		return 0, err
	}
	if _, err := s.remember(n, b, true); err != nil {
		return 0, fmt.Errorf("chain: finalized block timestamp: %w", err)
	}
	return n, nil
}

func (s *EVMRPCScanner) getBlock(ctx context.Context, tag string) (*rpcBlock, error) {
	raw, err := s.rpc.call(ctx, "eth_getBlockByNumber", []any{tag, false})
	if err != nil {
		return nil, err
	}
	if isJSONNull(raw) {
		return nil, fmt.Errorf("chain: eth_getBlockByNumber(%s) returned null", tag)
	}
	var b rpcBlock
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("chain: eth_getBlockByNumber(%s): %w", tag, err)
	}
	if b.Number == "" || b.Hash == "" || b.Timestamp == "" {
		return nil, fmt.Errorf("chain: eth_getBlockByNumber(%s): incomplete block", tag)
	}
	return &b, nil
}

func (s *EVMRPCScanner) remember(n uint64, b *rpcBlock, finalized bool) (blockHeader, error) {
	ts, err := parseHexUint64(b.Timestamp)
	if err != nil {
		return blockHeader{}, err
	}
	h := blockHeader{Hash: strings.ToLower(b.Hash), Time: time.Unix(int64(ts), 0).UTC(), Finalized: finalized}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.blockCache) >= evmBlockCacheSize {
		s.blockCache = map[uint64]blockHeader{}
	}
	if old, ok := s.blockCache[n]; !ok || !old.Finalized || finalized {
		s.blockCache[n] = h // never downgrade a finalized entry
	}
	return h, nil
}

// header returns hash+time of block n, verifying it matches wantHash (the
// hash the log/receipt claims). A mismatch means a reorg happened between the
// two reads and is an error. When finalized is true the caller has already
// checked n <= finalized head; a cached header that was fetched before block
// n was finalized is not trusted and is re-read from the node.
func (s *EVMRPCScanner) header(ctx context.Context, n uint64, wantHash string, finalized bool) (blockHeader, error) {
	s.mu.Lock()
	h, ok := s.blockCache[n]
	s.mu.Unlock()
	if !ok || h.Hash != wantHash || (finalized && !h.Finalized) {
		b, err := s.getBlock(ctx, "0x"+strconv.FormatUint(n, 16))
		if err != nil {
			return blockHeader{}, err
		}
		if got, err := parseHexUint64(b.Number); err != nil || got != n {
			return blockHeader{}, fmt.Errorf("chain: eth_getBlockByNumber(%d) returned block %q", n, b.Number)
		}
		// Use the header we just fetched, not whatever the shared cache holds
		// now (a concurrent non-finalized scan may have written to it).
		if h, err = s.remember(n, b, finalized); err != nil {
			return blockHeader{}, fmt.Errorf("chain: block %d timestamp: %w", n, err)
		}
	}
	if h.Hash != wantHash {
		return blockHeader{}, fmt.Errorf("chain: block %d hash %s != log block hash %s (reorg in progress?)", n, h.Hash, wantHash)
	}
	return h, nil
}

type rpcLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     *string  `json:"blockNumber"`
	BlockHash       *string  `json:"blockHash"`
	TransactionHash *string  `json:"transactionHash"`
	LogIndex        *string  `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

// ScanRange implements EVMScanner. When finalized is true the whole range must
// be at or below the finalized head, otherwise ErrNotFinalized is returned
// (the range is NOT silently clamped: a caller advancing its watermark to `to`
// would otherwise skip blocks).
func (s *EVMRPCScanner) ScanRange(ctx context.Context, from, to uint64, addresses []string, finalized bool) ([]Observation, error) {
	if from > to {
		return nil, fmt.Errorf("chain: invalid range [%d,%d]", from, to)
	}
	addrs, err := normalizeEVMAddressSet(addresses)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, nil
	}
	var finalizedHead uint64
	if finalized {
		finalizedHead, err = s.FinalizedBlock(ctx)
		if err != nil {
			return nil, err
		}
		if to > finalizedHead {
			return nil, fmt.Errorf("%w: range end %d > finalized head %d", ErrNotFinalized, to, finalizedHead)
		}
	}
	watched := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		watched[a] = struct{}{}
	}

	var logs []rpcLog
	for start := from; ; {
		end := to
		if end-start >= s.maxRange {
			end = start + s.maxRange - 1
		}
		for i := 0; i < len(addrs); i += s.maxAddresses {
			j := min(i+s.maxAddresses, len(addrs))
			got, err := s.getLogsAdaptive(ctx, start, end, addrs[i:j])
			if err != nil {
				return nil, err
			}
			logs = append(logs, got...)
		}
		if end == to {
			break
		}
		start = end + 1
	}
	obs, err := s.logsToObservations(ctx, logs, watched, finalized, finalizedHead)
	if err != nil {
		return nil, err
	}
	for _, o := range obs {
		if o.BlockNumber < from || o.BlockNumber > to {
			return nil, fmt.Errorf("chain: provider returned log at block %d outside [%d,%d]", o.BlockNumber, from, to)
		}
	}
	return obs, nil
}

// getLogsAdaptive bisects the block range, then the address list, on
// provider "too much data" errors. Any other error, or a single block and
// single address that still fails, is returned: never a partial result.
func (s *EVMRPCScanner) getLogsAdaptive(ctx context.Context, from, to uint64, addrs []string) ([]rpcLog, error) {
	logs, err := s.getLogs(ctx, from, to, addrs)
	if err == nil {
		return logs, nil
	}
	if !isTooMuchDataError(err) {
		return nil, err
	}
	if from < to {
		mid := from + (to-from)/2
		left, err := s.getLogsAdaptive(ctx, from, mid, addrs)
		if err != nil {
			return nil, err
		}
		right, err := s.getLogsAdaptive(ctx, mid+1, to, addrs)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	}
	if len(addrs) > 1 {
		half := len(addrs) / 2
		left, err := s.getLogsAdaptive(ctx, from, to, addrs[:half])
		if err != nil {
			return nil, err
		}
		right, err := s.getLogsAdaptive(ctx, from, to, addrs[half:])
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	}
	return nil, fmt.Errorf("chain: eth_getLogs still too large for single block %d and single address: %w", from, err)
}

func (s *EVMRPCScanner) getLogs(ctx context.Context, from, to uint64, addrs []string) ([]rpcLog, error) {
	padded := make([]string, len(addrs))
	for i, a := range addrs {
		padded[i] = "0x000000000000000000000000" + a[2:]
	}
	filter := map[string]any{
		"fromBlock": "0x" + strconv.FormatUint(from, 16),
		"toBlock":   "0x" + strconv.FormatUint(to, 16),
		"address":   s.contracts,
		"topics":    []any{TransferTopic0, nil, padded},
	}
	raw, err := s.rpc.call(ctx, "eth_getLogs", []any{filter})
	if err != nil {
		return nil, err
	}
	if isJSONNull(raw) {
		return nil, errors.New("chain: eth_getLogs returned null")
	}
	var logs []rpcLog
	if err := json.Unmarshal(raw, &logs); err != nil {
		return nil, fmt.Errorf("chain: eth_getLogs: %w", err)
	}
	return logs, nil
}

func isTooMuchDataError(err error) bool { return errors.Is(err, ErrTooMuchData) }

func (s *EVMRPCScanner) logsToObservations(ctx context.Context, logs []rpcLog, watched map[string]struct{}, finalized bool, finalizedHead uint64) ([]Observation, error) {
	topic0 := TransferTopic0
	seen := map[string]struct{}{}
	var out []Observation
	for _, l := range logs {
		if l.Removed {
			continue
		}
		contract := strings.ToLower(l.Address)
		asset, ok := s.assets[contract]
		if !ok {
			continue // not a whitelisted token contract
		}
		if len(l.Topics) == 0 || strings.ToLower(l.Topics[0]) != topic0 {
			continue // another event of the token contract (receipt path)
		}
		if len(l.Topics) != 3 {
			// A whitelisted ERC-20 always emits Transfer with 3 topics; any
			// other shape is a corrupt/incomplete response. Fail instead of
			// skipping so the caller's cursor cannot move past it.
			return nil, fmt.Errorf("chain: %s Transfer log in tx %v has %d topics, want 3", asset.Symbol, derefOr(l.TransactionHash), len(l.Topics))
		}
		to, err := topicToAddress(l.Topics[2])
		if err != nil {
			return nil, err
		}
		if _, ok := watched[to]; !ok {
			continue
		}
		from, err := topicToAddress(l.Topics[1])
		if err != nil {
			return nil, err
		}
		units, err := parseUint256Data(l.Data)
		if err != nil {
			return nil, fmt.Errorf("chain: whitelisted %s log has bad data: %w", asset.Symbol, err)
		}
		if units.Sign() <= 0 {
			continue
		}
		if l.BlockNumber == nil || l.BlockHash == nil || l.TransactionHash == nil || l.LogIndex == nil {
			return nil, errors.New("chain: log is missing block/tx/logIndex (pending log?)")
		}
		bn, err := parseHexUint64(*l.BlockNumber)
		if err != nil {
			return nil, err
		}
		li, err := parseHexUint64(*l.LogIndex)
		if err != nil {
			return nil, err
		}
		txHash := strings.ToLower(*l.TransactionHash)
		if !isHex32(txHash) {
			return nil, fmt.Errorf("chain: bad tx hash %q", txHash)
		}
		blockHash := strings.ToLower(*l.BlockHash)
		if finalized && bn > finalizedHead {
			return nil, fmt.Errorf("%w: log at block %d > finalized head %d", ErrNotFinalized, bn, finalizedHead)
		}
		key := txHash + "/" + strconv.FormatUint(li, 10)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		hdr, err := s.header(ctx, bn, blockHash, finalized)
		if err != nil {
			return nil, err
		}
		out = append(out, Observation{
			Network:     s.network,
			Asset:       asset,
			TxHash:      txHash,
			EventKey:    "log:" + strconv.FormatUint(li, 10),
			ToAddress:   to,
			FromAddress: from,
			Units:       units,
			BlockNumber: bn,
			BlockHash:   blockHash,
			BlockTime:   hdr.Time,
			Finalized:   finalized,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BlockNumber != out[j].BlockNumber {
			return out[i].BlockNumber < out[j].BlockNumber
		}
		return eventIndex(out[i].EventKey) < eventIndex(out[j].EventKey)
	})
	return out, nil
}

func eventIndex(key string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(key, "log:"), 10, 64)
	return n
}

type rpcReceipt struct {
	TransactionHash string   `json:"transactionHash"`
	BlockNumber     *string  `json:"blockNumber"`
	BlockHash       *string  `json:"blockHash"`
	Status          string   `json:"status"`
	Logs            []rpcLog `json:"logs"`
}

// ObservationsForTx implements EVMScanner via eth_getTransactionReceipt.
func (s *EVMRPCScanner) ObservationsForTx(ctx context.Context, txHash string, addresses []string, finalized bool) ([]Observation, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if !isHex32(txHash) {
		return nil, fmt.Errorf("chain: invalid tx hash %q", txHash)
	}
	addrs, err := normalizeEVMAddressSet(addresses)
	if err != nil {
		return nil, err
	}
	raw, err := s.rpc.call(ctx, "eth_getTransactionReceipt", []any{txHash})
	if err != nil {
		return nil, err
	}
	if isJSONNull(raw) {
		return nil, fmt.Errorf("%w: %s", ErrTxNotFound, txHash)
	}
	var rc rpcReceipt
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, fmt.Errorf("chain: eth_getTransactionReceipt: %w", err)
	}
	if rc.BlockNumber == nil || rc.BlockHash == nil {
		return nil, fmt.Errorf("%w: %s is pending", ErrNotFinalized, txHash)
	}
	bn, err := parseHexUint64(*rc.BlockNumber)
	if err != nil {
		return nil, err
	}
	var finalizedHead uint64
	if finalized {
		finalizedHead, err = s.FinalizedBlock(ctx)
		if err != nil {
			return nil, err
		}
		if bn > finalizedHead {
			return nil, fmt.Errorf("%w: tx block %d > finalized head %d", ErrNotFinalized, bn, finalizedHead)
		}
	}
	// Re-check the receipt's block is canonical even if no log matches.
	if !strings.EqualFold(rc.TransactionHash, txHash) {
		return nil, fmt.Errorf("chain: receipt is for %q, requested %s", rc.TransactionHash, txHash)
	}
	receiptBlockHash := strings.ToLower(*rc.BlockHash)
	if _, err := s.header(ctx, bn, receiptBlockHash, finalized); err != nil {
		return nil, err
	}
	switch rc.Status {
	case "0x1":
	case "0x0":
		return nil, nil // reverted: no transfer happened
	default:
		return nil, fmt.Errorf("chain: receipt %s has unexpected status %q", txHash, rc.Status)
	}
	if rc.Logs == nil {
		return nil, fmt.Errorf("chain: successful receipt %s has no logs field", txHash)
	}
	watched := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		watched[a] = struct{}{}
	}
	for i := range rc.Logs {
		// Receipt logs normally carry these; fill from the receipt if a node omits them.
		if rc.Logs[i].BlockNumber == nil {
			rc.Logs[i].BlockNumber = rc.BlockNumber
		}
		if rc.Logs[i].BlockHash == nil {
			rc.Logs[i].BlockHash = rc.BlockHash
		}
		if rc.Logs[i].TransactionHash == nil {
			h := txHash
			rc.Logs[i].TransactionHash = &h
		}
		if !strings.EqualFold(*rc.Logs[i].TransactionHash, txHash) || !strings.EqualFold(*rc.Logs[i].BlockHash, receiptBlockHash) ||
			!strings.EqualFold(*rc.Logs[i].BlockNumber, *rc.BlockNumber) {
			return nil, fmt.Errorf("chain: receipt %s contains a log from another tx/block", txHash)
		}
	}
	return s.logsToObservations(ctx, rc.Logs, watched, finalized, finalizedHead)
}

func normalizeEVMAddressSet(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		n, err := NormalizeEVMAddress(a)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func topicToAddress(topic string) (string, error) {
	t := strings.ToLower(topic)
	if len(t) != 66 || !strings.HasPrefix(t, "0x") {
		return "", fmt.Errorf("chain: bad address topic %q", topic)
	}
	if _, err := hex.DecodeString(t[2:]); err != nil {
		return "", fmt.Errorf("chain: bad address topic %q", topic)
	}
	if strings.Trim(t[2:26], "0") != "" {
		return "", fmt.Errorf("chain: address topic %q has non-zero high bytes", topic)
	}
	return "0x" + t[26:], nil
}

func parseUint256Data(data string) (*big.Int, error) {
	d := strings.ToLower(data)
	if !strings.HasPrefix(d, "0x") || len(d) != 66 {
		return nil, fmt.Errorf("transfer data %q is not one 32-byte word", data)
	}
	b, err := hex.DecodeString(d[2:])
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

func parseHexUint64(s string) (uint64, error) {
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return 0, fmt.Errorf("chain: %q is not 0x hex", s)
	}
	n, err := strconv.ParseUint(s[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("chain: bad hex quantity %q: %w", s, err)
	}
	return n, nil
}

func isHex32(s string) bool {
	if len(s) != 66 || !strings.HasPrefix(s, "0x") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

func derefOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
