package transport

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"llm-swap/internal/protocol"
)

func TestHKDFSHA256MatchesRFC5869TestCase1(t *testing.T) {
	ikm := make([]byte, 22)
	for i := range ikm {
		ikm[i] = 0x0b
	}
	salt := mustDecodeHex(t, "000102030405060708090a0b0c")
	info := mustDecodeHex(t, "f0f1f2f3f4f5f6f7f8f9")
	want := mustDecodeHex(t, "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")

	got := hkdfSHA256(ikm, salt, info, len(want))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HKDF output\n got: %x\nwant: %x", got, want)
	}
}

func TestBootstrapRoundTrip(t *testing.T) {
	payload := testBootstrap()

	envelope, err := SealBootstrap("agent-secret", "worker-gpu0", 42, payload)
	if err != nil {
		t.Fatalf("seal bootstrap: %v", err)
	}
	got, err := OpenBootstrap("agent-secret", "worker-gpu0", envelope)
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	if !reflect.DeepEqual(got, payload) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, payload)
	}
}

func TestSealBootstrapUsesFreshNonceAndHidesSecrets(t *testing.T) {
	payload := testBootstrap()

	first, err := SealBootstrap("agent-secret", "worker-gpu0", 42, payload)
	if err != nil {
		t.Fatalf("first seal: %v", err)
	}
	second, err := SealBootstrap("agent-secret", "worker-gpu0", 42, payload)
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if first.Nonce == second.Nonce {
		t.Fatal("two seals reused a nonce")
	}
	decodedNonce, err := base64.RawURLEncoding.DecodeString(first.Nonce)
	if err != nil {
		t.Fatalf("nonce is not raw URL-safe base64: %v", err)
	}
	if len(decodedNonce) != 12 {
		t.Fatalf("nonce length = %d, want 12", len(decodedNonce))
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{payload.AuthToken, payload.LlamaSwapToken} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("encrypted envelope contains plaintext secret %q", secret)
		}
	}
}

func TestOpenBootstrapRejectsWrongBindingAndMalformedEnvelope(t *testing.T) {
	const (
		agentToken = "agent-secret"
		agentID    = "worker-gpu0"
	)
	payload := testBootstrap()
	envelope, err := SealBootstrap(agentToken, agentID, 42, payload)
	if err != nil {
		t.Fatalf("seal bootstrap: %v", err)
	}

	tamperedNonce := envelope
	nonceBytes, err := base64.RawURLEncoding.DecodeString(tamperedNonce.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	nonceBytes[0] ^= 0xff
	tamperedNonce.Nonce = base64.RawURLEncoding.EncodeToString(nonceBytes)

	tamperedCiphertext := envelope
	ciphertextBytes, err := base64.RawURLEncoding.DecodeString(tamperedCiphertext.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertextBytes[len(ciphertextBytes)-1] ^= 0xff
	tamperedCiphertext.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertextBytes)

	wrongGeneration := envelope
	wrongGeneration.Generation++
	invalidNonceBase64 := envelope
	invalidNonceBase64.Nonce = "%not-base64"
	invalidCiphertextBase64 := envelope
	invalidCiphertextBase64.Ciphertext = "%not-base64"
	shortNonce := envelope
	shortNonce.Nonce = base64.RawURLEncoding.EncodeToString([]byte{1})
	shortCiphertext := envelope
	shortCiphertext.Ciphertext = base64.RawURLEncoding.EncodeToString([]byte{1})

	tests := []struct {
		name     string
		token    string
		id       string
		envelope protocol.EncryptedTransportBootstrap
	}{
		{name: "wrong agent id", token: agentToken, id: "worker-gpu1", envelope: envelope},
		{name: "wrong agent token", token: "wrong-agent-secret", id: agentID, envelope: envelope},
		{name: "wrong generation", token: agentToken, id: agentID, envelope: wrongGeneration},
		{name: "tampered nonce", token: agentToken, id: agentID, envelope: tamperedNonce},
		{name: "tampered ciphertext", token: agentToken, id: agentID, envelope: tamperedCiphertext},
		{name: "invalid nonce base64", token: agentToken, id: agentID, envelope: invalidNonceBase64},
		{name: "invalid ciphertext base64", token: agentToken, id: agentID, envelope: invalidCiphertextBase64},
		{name: "short nonce", token: agentToken, id: agentID, envelope: shortNonce},
		{name: "short ciphertext", token: agentToken, id: agentID, envelope: shortCiphertext},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := OpenBootstrap(tt.token, tt.id, tt.envelope)
			if err == nil {
				t.Fatal("open bootstrap succeeded")
			}
			for _, secret := range []string{agentToken, tt.token, agentID, tt.id, payload.AuthToken, payload.LlamaSwapToken} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks input %q: %v", secret, err)
				}
			}
		})
	}
}

func TestBootstrapRejectsEmptyAgentCredentials(t *testing.T) {
	tests := []struct {
		name  string
		token string
		id    string
	}{
		{name: "empty token", id: "worker-gpu0"},
		{name: "empty id", token: "agent-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SealBootstrap(tt.token, tt.id, 42, testBootstrap()); err == nil {
				t.Fatal("seal bootstrap succeeded")
			}
			if _, err := OpenBootstrap(tt.token, tt.id, protocol.EncryptedTransportBootstrap{}); err == nil {
				t.Fatal("open bootstrap succeeded")
			}
		})
	}
}

func testBootstrap() Bootstrap {
	return Bootstrap{
		Type:            "frp_tcp",
		ServerAddr:      "frps.example.test",
		ServerPort:      7000,
		AuthToken:       "frp-secret",
		PortStart:       2000,
		PortEnd:         3000,
		LeaseTTLSeconds: 180,
		LlamaSwapToken:  "llama-swap-secret",
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
