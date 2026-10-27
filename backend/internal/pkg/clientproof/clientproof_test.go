package clientproof

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// vector is generated independently (Python cryptography); see testdata.
type vector struct {
	PrivateKeyPKCS8PEM string `json:"private_key_pkcs8_pem"`
	PublicKey          string `json:"public_key"`
	APIKey             string `json:"api_key"`
	DeviceID           string `json:"device_id"`
	Method             string `json:"method"`
	Target             string `json:"target"`
	TS                 int64  `json:"ts"`
	Nonce              string `json:"nonce"`
	Message            string `json:"message"`
	Signature          string `json:"signature"`
	Header             string `json:"header"`
}

func loadVector(t *testing.T) vector {
	t.Helper()
	data, err := os.ReadFile("testdata/client-proof-v1-vector.json")
	require.NoError(t, err)
	var v vector
	require.NoError(t, json.Unmarshal(data, &v))
	return v
}

func TestVectorMessageAndSignature(t *testing.T) {
	v := loadVector(t)
	require.Equal(t, v.Message, string(Message(v.Method, v.Target, v.TS, v.Nonce, v.APIKey)))

	proof, err := ParseHeader(v.Header)
	require.NoError(t, err)
	require.Equal(t, v.DeviceID, proof.DeviceID)
	require.Equal(t, v.TS, proof.Timestamp)
	require.Equal(t, v.Nonce, proof.Nonce)

	key, raw, err := DecodePublicKey(v.PublicKey)
	require.NoError(t, err)
	require.Len(t, raw, 65)

	at := time.Unix(v.TS, 0)
	require.Equal(t, StatusValid, Verify(proof, key, v.Method, v.Target, v.APIKey, at))
	// Method case is normalized to uppercase when building the message.
	require.Equal(t, StatusValid, Verify(proof, key, strings.ToLower(v.Method), v.Target, v.APIKey, at))
	// Window is inclusive at ±300s.
	require.Equal(t, StatusValid, Verify(proof, key, v.Method, v.Target, v.APIKey, at.Add(300*time.Second)))
	require.Equal(t, StatusValid, Verify(proof, key, v.Method, v.Target, v.APIKey, at.Add(-300*time.Second)))
}

func TestVectorPrivateKeyMatchesPublicKey(t *testing.T) {
	v := loadVector(t)
	block, _ := pem.Decode([]byte(v.PrivateKeyPKCS8PEM))
	require.NotNil(t, block)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	priv, ok := parsed.(*ecdsa.PrivateKey)
	require.True(t, ok)
	pub, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	require.Equal(t, v.PublicKey, base64.RawURLEncoding.EncodeToString(pub))
}

func TestVectorNegativeCases(t *testing.T) {
	v := loadVector(t)
	proof, err := ParseHeader(v.Header)
	require.NoError(t, err)
	key, _, err := DecodePublicKey(v.PublicKey)
	require.NoError(t, err)
	at := time.Unix(v.TS, 0)

	changedTarget := []byte(v.Target)
	changedTarget[len(changedTarget)-1] ^= 0x01
	require.Equal(t, StatusInvalid, Verify(proof, key, v.Method, string(changedTarget), v.APIKey, at), "1-byte target change")
	require.Equal(t, StatusInvalid, Verify(proof, key, v.Method, v.Target, v.APIKey+"x", at), "different API key")
	require.Equal(t, StatusInvalid, Verify(proof, key, "GET", v.Target, v.APIKey, at), "different method")
	require.Equal(t, StatusStale, Verify(proof, key, v.Method, v.Target, v.APIKey, at.Add(301*time.Second)), "ts too old")
	require.Equal(t, StatusStale, Verify(proof, key, v.Method, v.Target, v.APIKey, at.Add(-301*time.Second)), "ts in the future")

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	require.Equal(t, StatusInvalid, Verify(proof, &other.PublicKey, v.Method, v.Target, v.APIKey, at), "other device key")

	tampered := proof
	tampered.Nonce = "AAECAwQFBgcICQoLDA0OEA"
	require.Equal(t, StatusInvalid, Verify(tampered, key, v.Method, v.Target, v.APIKey, at), "nonce is signed")
	tampered = proof
	tampered.Timestamp++
	require.Equal(t, StatusInvalid, Verify(tampered, key, v.Method, v.Target, v.APIKey, at), "ts is signed")
	require.Equal(t, StatusInvalid, Verify(proof, nil, v.Method, v.Target, v.APIKey, at))
}

func TestParseHeaderRejectsMalformed(t *testing.T) {
	v := loadVector(t)
	parts := strings.Split(v.Header, ".")
	join := func(p ...string) string { return strings.Join(p, ".") }
	cases := map[string]string{
		"empty":           "",
		"too long":        v.Header + strings.Repeat("A", MaxHeaderLength),
		"wrong version":   join("v2", parts[1], parts[2], parts[3], parts[4]),
		"extra part":      v.Header + ".x",
		"missing part":    join(parts[0], parts[1], parts[2], parts[3]),
		"bad device":      join(parts[0], "dev_AAAAAAAAAAAAAAAAAAAAAA", parts[2], parts[3], parts[4]),
		"short device":    join(parts[0], "cd_AAAA", parts[2], parts[3], parts[4]),
		"signed ts":       join(parts[0], parts[1], "+"+parts[2], parts[3], parts[4]),
		"leading zero ts": join(parts[0], parts[1], "0"+parts[2], parts[3], parts[4]),
		"hex ts":          join(parts[0], parts[1], "0x10", parts[3], parts[4]),
		"short nonce":     join(parts[0], parts[1], parts[2], "AAEC", parts[4]),
		"padded nonce":    join(parts[0], parts[1], parts[2], parts[3]+"==", parts[4]),
		"std b64 sig":     join(parts[0], parts[1], parts[2], parts[3], strings.ReplaceAll(strings.ReplaceAll(parts[4], "-", "+"), "_", "/")),
		"short sig":       join(parts[0], parts[1], parts[2], parts[3], parts[4][:40]),
		"whitespace":      " " + v.Header,
	}
	for name, header := range cases {
		_, err := ParseHeader(header)
		require.ErrorIs(t, err, ErrMalformed, name)
	}
}

func TestDecodePublicKeyRejectsNonUncompressed(t *testing.T) {
	v := loadVector(t)
	_, raw, err := DecodePublicKey(v.PublicKey)
	require.NoError(t, err)

	compressed := append([]byte{0x02 | (raw[64] & 1)}, raw[1:33]...)
	_, _, err = DecodePublicKey(base64.RawURLEncoding.EncodeToString(compressed))
	require.ErrorIs(t, err, ErrMalformed, "compressed point")

	offCurve := append([]byte(nil), raw...)
	offCurve[64] ^= 0x01
	_, _, err = DecodePublicKey(base64.RawURLEncoding.EncodeToString(offCurve))
	require.ErrorIs(t, err, ErrMalformed, "point not on curve")

	_, _, err = DecodePublicKey(base64.StdEncoding.EncodeToString(raw))
	require.ErrorIs(t, err, ErrMalformed, "padded std base64")

	_, _, err = DecodePublicKey("")
	require.ErrorIs(t, err, ErrMalformed)
}

func TestRoundTripWithGeneratedKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	deviceID, err := NewDeviceID()
	require.NoError(t, err)
	require.True(t, ValidDeviceID(deviceID))
	require.Len(t, deviceID, 25)

	header, err := Sign(priv, deviceID, "GET", "/v1/responses", 1_800_000_000, "AAECAwQFBgcICQoLDA0ODw", "sk-live")
	require.NoError(t, err)
	require.LessOrEqual(t, len(header), MaxHeaderLength)
	proof, err := ParseHeader(header)
	require.NoError(t, err)
	require.Equal(t, StatusValid, Verify(proof, &priv.PublicKey, "GET", "/v1/responses", "sk-live", time.Unix(1_800_000_010, 0)))
}

func TestStatusNamesAreStable(t *testing.T) {
	want := []string{"valid", "missing", "invalid", "stale", "replay", "unknown_device", "revoked", "unverified"}
	for i, status := range AllStatuses() {
		require.Equal(t, Status(i+1), status)
		require.Equal(t, want[i], status.String())
	}
	require.False(t, Status(9).Known())
}
