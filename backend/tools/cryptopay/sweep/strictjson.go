package sweep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
)

// MaxDocumentBytes bounds every JSON document this package decodes (plans,
// signed bundles, the offline ledger).
const MaxDocumentBytes = 4 << 20

// DecodeStrict decodes exactly one JSON value into v and rejects: input larger
// than MaxDocumentBytes, unknown fields, duplicate object keys at any depth,
// and anything but whitespace after the value.
func DecodeStrict(data []byte, v any) error {
	return decodeStrictLimit(data, v, MaxDocumentBytes)
}

func decodeStrictLimit(data []byte, v any, limit int) error {
	if len(data) > limit {
		return fmt.Errorf("sweep: document is %d bytes, limit %d", len(data), limit)
	}
	if err := checkKeys(data, jsonFieldNames(reflect.TypeOf(v))); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("sweep: decode: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("sweep: trailing data after JSON document")
	}
	return nil
}

// ReadLimited reads at most MaxDocumentBytes from r; larger input is an error.
func ReadLimited(r io.Reader) ([]byte, error) { return readLimit(r, MaxDocumentBytes) }

func readLimit(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("sweep: input larger than %d bytes", limit)
	}
	return data, nil
}

// jsonFieldNames collects every JSON field name reachable from t (struct
// tags, through pointers and slices).
func jsonFieldNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			names[name] = true
			walk(f.Type)
		}
	}
	walk(t)
	return names
}

// checkNoDuplicateKeys is checkKeys without a schema (tests).
func checkNoDuplicateKeys(data []byte) error { return checkKeys(data, nil) }

// checkKeys walks the token stream and fails on a repeated key in any object
// (encoding/json silently keeps the last one) and, when allowed is non-nil,
// on any key that is not byte-equal to a schema field name: encoding/json
// matches names case-insensitively with Unicode simple folding ("Amount",
// "gaſ_limit" with U+017F), which must not reach a field.
func checkKeys(data []byte, allowed map[string]bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		object  bool
		keys    map[string]struct{}
		wantKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sweep: decode: %w", err)
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.object {
					top.wantKey = true
				}
				stack = append(stack, &frame{object: true, keys: map[string]struct{}{}, wantKey: true})
				if len(stack) > 64 {
					return errors.New("sweep: JSON nested too deeply")
				}
			case '[':
				if top != nil && top.object {
					top.wantKey = true
				}
				stack = append(stack, &frame{})
				if len(stack) > 64 {
					return errors.New("sweep: JSON nested too deeply")
				}
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		case string:
			if top != nil && top.object && top.wantKey {
				// encoding/json matches field names case-insensitively, so
				// "amount" and "Amount" are duplicates too.
				if allowed != nil && !allowed[t] {
					return fmt.Errorf("sweep: unknown JSON key %s", clip(t))
				}
				k := strings.ToLower(t)
				if _, dup := top.keys[k]; dup {
					return fmt.Errorf("sweep: duplicate JSON key %s", clip(t))
				}
				top.keys[k] = struct{}{}
				top.wantKey = false
				continue
			}
			if top != nil && top.object {
				top.wantKey = true
			}
		default:
			if top != nil && top.object {
				top.wantKey = true
			}
		}
	}
}

// ParseCanonicalUint parses a canonical non-negative decimal integer string:
// digits only, no sign, no leading zeros (except "0" itself), at most 78
// digits (fits uint256).
func ParseCanonicalUint(s string) (*big.Int, error) {
	if s == "" || len(s) > 78 {
		return nil, fmt.Errorf("sweep: invalid integer %s", clip(s))
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("sweep: invalid integer %s", clip(s))
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return nil, fmt.Errorf("sweep: non-canonical integer %s", clip(s))
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.BitLen() > 256 {
		return nil, fmt.Errorf("sweep: integer out of range %s", clip(s))
	}
	return v, nil
}

// ParsePositiveUint is ParseCanonicalUint that also rejects zero.
func ParsePositiveUint(s string) (*big.Int, error) {
	v, err := ParseCanonicalUint(s)
	if err != nil {
		return nil, err
	}
	if v.Sign() == 0 {
		return nil, fmt.Errorf("sweep: zero is not allowed here")
	}
	return v, nil
}

// ParseUint64 parses a canonical decimal string into a uint64.
func ParseUint64(s string) (uint64, error) {
	v, err := ParseCanonicalUint(s)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("sweep: integer %s exceeds uint64", clip(s))
	}
	return v.Uint64(), nil
}

// ParseTokenAmount converts a human decimal amount ("2.7", "10", "0.000001")
// into raw units with the given decimals. At most `decimals` fractional
// digits; no sign, exponent, spaces or leading zeros in the integer part.
func ParseTokenAmount(s string, decimals int) (*big.Int, error) {
	if s == "" || len(s) > 40 {
		return nil, fmt.Errorf("sweep: invalid amount %s", clip(s))
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if hasDot && frac == "" {
		return nil, fmt.Errorf("sweep: invalid amount %s", clip(s))
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("sweep: amount %s has more than %d decimals", clip(s), decimals)
	}
	if _, err := ParseCanonicalUint(intPart); err != nil {
		return nil, fmt.Errorf("sweep: invalid amount %s", clip(s))
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("sweep: invalid amount %s", clip(s))
		}
	}
	raw, ok := new(big.Int).SetString(intPart+frac+strings.Repeat("0", decimals-len(frac)), 10)
	if !ok {
		return nil, fmt.Errorf("sweep: invalid amount %s", clip(s))
	}
	if raw.Sign() == 0 {
		return nil, errors.New("sweep: amount must be > 0")
	}
	return raw, nil
}

// FormatUnits renders raw units with the given decimals ("2700000",6 -> "2.700000").
func FormatUnits(v *big.Int, decimals int) string {
	if v == nil {
		return "0"
	}
	neg := v.Sign() < 0
	s := new(big.Int).Abs(v).String()
	if decimals > 0 {
		if len(s) <= decimals {
			s = strings.Repeat("0", decimals-len(s)+1) + s
		}
		s = s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func clip(s string) string {
	if len(s) > 48 {
		s = s[:48] + "..."
	}
	return fmt.Sprintf("%q", s)
}
