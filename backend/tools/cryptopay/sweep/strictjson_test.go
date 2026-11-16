package sweep

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodePlanStrict(t *testing.T) {
	good, err := json.Marshal(evmSweepPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(good); err != nil {
		t.Fatalf("good plan: %v", err)
	}
	s := string(good)
	bad := map[string]string{
		"unknown top field":    strings.Replace(s, `"version":1`, `"version":1,"to":"0xevil"`, 1),
		"unknown nested field": strings.Replace(s, `"symbol":"USDT"`, `"symbol":"USDT","recipient":"0xevil"`, 1),
		"duplicate key":        strings.Replace(s, `"treasury":`, `"treasury":"0x0000000000000000000000000000000000000001","treasury":`, 1),
		"duplicate nested key": strings.Replace(s, `"amount":"2700000"`, `"amount":"1","amount":"2700000"`, 1),
		"trailing data":        s + `{"version":1}`,
		"trailing garbage":     s + "x",
		"two documents":        s + "\n" + s,
		"number amount":        strings.Replace(s, `"amount":"2700000"`, `"amount":2700000`, 1),
		"float nonce":          strings.Replace(s, `"treasury_nonce":7`, `"treasury_nonce":7.5`, 1),
		"negative nonce":       strings.Replace(s, `"treasury_nonce":7`, `"treasury_nonce":-1`, 1),
		"index overflow":       strings.Replace(s, `"index":1,`, `"index":4294967296,`, 1),
		"version 2":            strings.Replace(s, `"version":1`, `"version":2`, 1),
		"unknown kind":         strings.Replace(s, `"kind":"sweep"`, `"kind":"drain"`, 1),
		"solana section too":   strings.Replace(s, `"evm":{`, `"solana":{"treasury":"x","cu_price_micro_lamports":"0"},"evm":{`, 1),
		"testnet":              strings.Replace(s, `"arbitrum-one"`, `"arbitrum-sepolia"`, 1),
		"oversized":            s + strings.Repeat(" ", MaxDocumentBytes),
		"not object":           `[]`,
	}
	for name, body := range bad {
		if _, err := DecodePlan([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Whitespace around the document is fine.
	if _, err := DecodePlan([]byte("\n " + s + " \n")); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateKeyDetectorShapes(t *testing.T) {
	ok := []string{`{"a":{"a":1},"b":[{"a":1},{"a":2}]}`, `{"a":[1,2,{"b":{}}],"c":"a"}`, `{"a":"b","b":"a"}`}
	for _, s := range ok {
		if err := checkNoDuplicateKeys([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := []string{`{"a":1,"a":2}`, `{"x":{"a":[],"a":[]}}`, `[{"a":1},{"b":{"c":1,"c":1}}]`, `{"a":{},"b":1,"a":1}`}
	for _, s := range bad {
		if err := checkNoDuplicateKeys([]byte(s)); err == nil {
			t.Errorf("%s: duplicate not detected", s)
		}
	}
}

func TestParseTokenAmount(t *testing.T) {
	good := map[string]string{"2.7": "2700000", "10": "10000000", "0.000001": "1", "1.000000": "1000000", "0.5": "500000"}
	for in, want := range good {
		v, err := ParseTokenAmount(in, 6)
		if err != nil || v.String() != want {
			t.Errorf("%s -> %v %v", in, v, err)
		}
	}
	for _, in := range []string{"", "0", "0.0", "1.", ".5", "-1", "+1", "1e3", "01", "1.0000001", "1,5", " 1", "1 ", "NaN"} {
		if _, err := ParseTokenAmount(in, 6); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if FormatUnits(bigS("2700000"), 6) != "2.700000" || FormatUnits(bigS("1"), 6) != "0.000001" {
		t.Fatal("FormatUnits")
	}
}

func TestDuplicateKeysCaseInsensitive(t *testing.T) {
	if err := checkNoDuplicateKeys([]byte(`{"amount":"1","Amount":"2"}`)); err == nil {
		t.Fatal("case-variant duplicate accepted")
	}
}

func TestOnlyExactSchemaKeys(t *testing.T) {
	good, _ := json.Marshal(evmSweepPlan(t))
	for name, body := range map[string]string{
		"capitalized":     strings.Replace(string(good), `"amount":`, `"Amount":`, 1),
		"long s (U+017F)": strings.Replace(string(good), `"gas_limit":`, `"gaſ_limit":`, 1),
		"kelvin sign":     strings.Replace(string(good), `"kind":`, `"\u212aind":`, 1),
		"upper top level": strings.Replace(string(good), `"version":`, `"VERSION":`, 1),
	} {
		if _, err := DecodePlan([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// encoding/json alone would accept the folded key (documents why the check exists).
	var p Plan
	if err := json.Unmarshal([]byte(strings.Replace(string(good), `"kind":`, `"KIND":`, 1)), &p); err != nil || p.Kind != KindSweep {
		t.Fatal("expected encoding/json to fold keys")
	}
	l, _ := json.Marshal(NewLedger())
	if _, err := DecodeLedger([]byte(strings.Replace(string(l), `"ops":`, `"Ops":`, 1))); err == nil {
		t.Fatal("ledger with folded key accepted")
	}
}
