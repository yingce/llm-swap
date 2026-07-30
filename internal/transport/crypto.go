package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"llm-swap/internal/protocol"
)

const bootstrapKeyLength = 32

var (
	errInvalidBootstrapInput = errors.New("invalid transport bootstrap input")
	errInvalidBootstrap      = errors.New("invalid encrypted transport bootstrap")
)

type Bootstrap struct {
	Type            string `json:"type"`
	ServerAddr      string `json:"server_addr"`
	ServerPort      int    `json:"server_port"`
	AuthToken       string `json:"auth_token"`
	PortStart       int    `json:"port_start"`
	PortEnd         int    `json:"port_end"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds"`
	LlamaSwapToken  string `json:"llama_swap_token"`
}

func SealBootstrap(agentToken, agentID string, generation uint64, payload Bootstrap) (protocol.EncryptedTransportBootstrap, error) {
	if agentToken == "" || agentID == "" {
		return protocol.EncryptedTransportBootstrap{}, errInvalidBootstrapInput
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return protocol.EncryptedTransportBootstrap{}, errors.New("could not encode transport bootstrap")
	}
	gcm, err := newBootstrapGCM(agentToken, agentID, generation)
	if err != nil {
		return protocol.EncryptedTransportBootstrap{}, errors.New("could not initialize transport bootstrap encryption")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return protocol.EncryptedTransportBootstrap{}, errors.New("could not seal transport bootstrap")
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, bootstrapAAD(agentID, generation))

	return protocol.EncryptedTransportBootstrap{
		Generation: generation,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func OpenBootstrap(agentToken, agentID string, envelope protocol.EncryptedTransportBootstrap) (Bootstrap, error) {
	if agentToken == "" || agentID == "" {
		return Bootstrap{}, errInvalidBootstrapInput
	}

	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return Bootstrap{}, errInvalidBootstrap
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return Bootstrap{}, errInvalidBootstrap
	}
	gcm, err := newBootstrapGCM(agentToken, agentID, envelope.Generation)
	if err != nil || len(nonce) != gcm.NonceSize() || len(ciphertext) < gcm.Overhead() {
		return Bootstrap{}, errInvalidBootstrap
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, bootstrapAAD(agentID, envelope.Generation))
	if err != nil {
		return Bootstrap{}, errInvalidBootstrap
	}

	var payload Bootstrap
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return Bootstrap{}, errInvalidBootstrap
	}
	return payload, nil
}

func newBootstrapGCM(agentToken, agentID string, generation uint64) (cipher.AEAD, error) {
	var salt [8]byte
	binary.BigEndian.PutUint64(salt[:], generation)
	key := hkdfSHA256([]byte(agentToken), salt[:], []byte("llmswap/transport-bootstrap/"+agentID), bootstrapKeyLength)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func hkdfSHA256(inputKeyMaterial, salt, info []byte, length int) []byte {
	extract := hmac.New(sha256.New, salt)
	_, _ = extract.Write(inputKeyMaterial)
	pseudorandomKey := extract.Sum(nil)

	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		expand := hmac.New(sha256.New, pseudorandomKey)
		_, _ = expand.Write(previous)
		_, _ = expand.Write(info)
		_, _ = expand.Write([]byte{counter})
		previous = expand.Sum(nil)
		result = append(result, previous...)
	}
	return result[:length]
}

func bootstrapAAD(agentID string, generation uint64) []byte {
	return []byte(fmt.Sprintf("%s\n%d", agentID, generation))
}
