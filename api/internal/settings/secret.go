package settings

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// secretVersion is stamped on every stored secret so a future format change can be
// migrated rather than guessed at.
const secretVersion = 1

// hintEdge is how many leading and trailing characters a hint reveals.
const hintEdge = 4

// minHintable is the shortest secret that gets a hint at all. Below this, showing
// four characters from each end would show most of the value.
const minHintable = 16

// ErrNotEncrypted means a stored value is not in the secret envelope format.
var ErrNotEncrypted = errors.New("settings: value is not an encrypted secret")

// Secret is the stored form of a secret setting.
//
// The plaintext is never part of this type, so a code path that has a Secret cannot
// accidentally serialise the real value. The read path returns Hint and IsSet, which
// is what the API exposes (F-1.8).
type Secret struct {
	Version int    `json:"v"`
	Sealed  []byte `json:"sealed"`

	// Hint shows the first and last few characters, enough for an admin to
	// recognise which key is stored without revealing it.
	Hint string `json:"hint,omitempty"`

	UpdatedAt time.Time `json:"updatedAt"`
}

// Cipher encrypts and decrypts secret settings with AES-256-GCM.
//
// GCM is authenticated, so a tampered ciphertext fails to open rather than
// decrypting to rubbish. The setting key is bound in as additional data, which means
// a ciphertext copied from one key's row to another will not open: without that,
// somebody with database access could move a known-plaintext secret into a
// higher-value key.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds the cipher from APP_ENCRYPTION_KEY.
func NewCipher(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("settings: build aes cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settings: build gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext for one setting key.
func (c *Cipher) Encrypt(settingKey, plaintext string) (Secret, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Secret{}, fmt.Errorf("settings: generate nonce: %w", err)
	}

	// The nonce is prepended to the sealed output. It is not secret; it only has to
	// be unique per key, and a random 96-bit nonce is safe at this volume.
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(settingKey))

	return Secret{
		Version:   secretVersion,
		Sealed:    sealed,
		Hint:      Hint(plaintext),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// Decrypt opens a secret. It is called at the point of use and the result is never
// held in a long-lived variable (backend-standards.md 6).
func (c *Cipher) Decrypt(settingKey string, secret Secret) (string, error) {
	if secret.Version != secretVersion {
		return "", fmt.Errorf("settings: unsupported secret version %d", secret.Version)
	}

	nonceSize := c.aead.NonceSize()
	if len(secret.Sealed) < nonceSize {
		return "", ErrNotEncrypted
	}

	nonce, ciphertext := secret.Sealed[:nonceSize], secret.Sealed[nonceSize:]

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(settingKey))
	if err != nil {
		// Deliberately vague. The cause is a wrong key, a tampered value, or a
		// ciphertext from a different setting, and telling them apart helps an
		// attacker more than an operator.
		return "", fmt.Errorf("settings: cannot decrypt %q: check APP_ENCRYPTION_KEY", settingKey)
	}
	return string(plaintext), nil
}

// Reseal re-encrypts a secret under this cipher, given the plaintext recovered with
// the old one. It is the key-rotation path.
func (c *Cipher) Reseal(settingKey, plaintext string) (Secret, error) {
	return c.Encrypt(settingKey, plaintext)
}

// Hint renders the recognisable fragment of a secret.
//
// Short values get no hint: revealing four characters from each end of a 10
// character value reveals nearly all of it.
func Hint(plaintext string) string {
	if utf8.RuneCountInString(plaintext) < minHintable {
		return ""
	}

	runes := []rune(plaintext)
	return string(runes[:hintEdge]) + "…" + string(runes[len(runes)-hintEdge:])
}

// EncodeSecret marshals a secret for the jsonb column.
func EncodeSecret(secret Secret) (json.RawMessage, error) {
	encoded, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("settings: encode secret: %w", err)
	}
	return encoded, nil
}

// DecodeSecret reads a secret back out of the jsonb column.
//
// DisallowUnknownFields is deliberate: a drifted payload is an error rather than a
// silently missing field (backend-standards.md 9).
func DecodeSecret(raw json.RawMessage) (Secret, error) {
	var secret Secret
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&secret); err != nil {
		return Secret{}, ErrNotEncrypted
	}
	if secret.Version == 0 || len(secret.Sealed) == 0 {
		return Secret{}, ErrNotEncrypted
	}
	return secret, nil
}
