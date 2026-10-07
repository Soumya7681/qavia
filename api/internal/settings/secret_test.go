package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()

	cipher, err := NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	return cipher
}

const apiKey = "sk-ant-api03-DO-NOT-LOG-ME-0123456789"

func TestEncryptAndDecrypt(t *testing.T) {
	cipher := testCipher(t)

	secret, err := cipher.Encrypt("anthropic.api_key", apiKey)
	require.NoError(t, err)
	require.Equal(t, secretVersion, secret.Version)
	require.NotEmpty(t, secret.Sealed)

	// The plaintext must not be recoverable from the stored bytes.
	require.NotContains(t, string(secret.Sealed), apiKey)

	plaintext, err := cipher.Decrypt("anthropic.api_key", secret)
	require.NoError(t, err)
	require.Equal(t, apiKey, plaintext)
}

// A random nonce per encryption means the same value seals differently every time,
// so equal ciphertexts cannot reveal that two providers share a key.
func TestCiphertextIsNotDeterministic(t *testing.T) {
	cipher := testCipher(t)

	first, err := cipher.Encrypt("k.one", apiKey)
	require.NoError(t, err)
	second, err := cipher.Encrypt("k.one", apiKey)
	require.NoError(t, err)

	require.NotEqual(t, first.Sealed, second.Sealed)
}

// The setting key is bound in as additional data, so a ciphertext moved between rows
// will not open. Without it, somebody with database access could promote a
// known-plaintext secret into a higher-value key.
func TestCiphertextIsBoundToItsSettingKey(t *testing.T) {
	cipher := testCipher(t)

	secret, err := cipher.Encrypt("notifications.slack_token", apiKey)
	require.NoError(t, err)

	_, err = cipher.Decrypt("storage.secret_key", secret)
	require.ErrorContains(t, err, "cannot decrypt")
}

// GCM is authenticated, so a tampered value fails to open rather than decrypting to
// rubbish that then gets used as a credential.
func TestTamperedCiphertextIsRejected(t *testing.T) {
	cipher := testCipher(t)

	secret, err := cipher.Encrypt("k.one", apiKey)
	require.NoError(t, err)

	secret.Sealed[len(secret.Sealed)-1] ^= 0xFF

	_, err = cipher.Decrypt("k.one", secret)
	require.ErrorContains(t, err, "cannot decrypt")
}

func TestWrongKeyCannotDecrypt(t *testing.T) {
	secret, err := testCipher(t).Encrypt("k.one", apiKey)
	require.NoError(t, err)

	other, err := NewCipher([]byte("ffffffffffffffffffffffffffffffff"))
	require.NoError(t, err)

	_, err = other.Decrypt("k.one", secret)
	require.ErrorContains(t, err, "APP_ENCRYPTION_KEY")
}

// The error must not distinguish a wrong key from a tampered value: telling them
// apart helps an attacker more than an operator.
func TestDecryptFailureIsVague(t *testing.T) {
	cipher := testCipher(t)
	secret, err := cipher.Encrypt("k.one", apiKey)
	require.NoError(t, err)

	tampered := secret
	tampered.Sealed = append([]byte(nil), secret.Sealed...)
	tampered.Sealed[0] ^= 0xFF

	wrongKeyCipher, err := NewCipher([]byte("ffffffffffffffffffffffffffffffff"))
	require.NoError(t, err)

	_, tamperErr := cipher.Decrypt("k.one", tampered)
	_, keyErr := wrongKeyCipher.Decrypt("k.one", secret)

	require.Equal(t, tamperErr.Error(), keyErr.Error())
}

func TestRotationReEncryptsUnderTheNewKey(t *testing.T) {
	old := testCipher(t)
	next, err := NewCipher([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	require.NoError(t, err)

	sealed, err := old.Encrypt("k.one", apiKey)
	require.NoError(t, err)

	// Rotation is: open with the old key, reseal with the new one.
	plaintext, err := old.Decrypt("k.one", sealed)
	require.NoError(t, err)

	rotated, err := next.Reseal("k.one", plaintext)
	require.NoError(t, err)

	recovered, err := next.Decrypt("k.one", rotated)
	require.NoError(t, err)
	require.Equal(t, apiKey, recovered)

	// The old key must no longer open it, or rotation achieved nothing.
	_, err = old.Decrypt("k.one", rotated)
	require.Error(t, err)
}

func TestHintRevealsOnlyTheEdges(t *testing.T) {
	hint := Hint(apiKey)

	require.Equal(t, "sk-a…6789", hint)
	require.NotContains(t, apiKey, hint)
	require.Less(t, len(hint), len(apiKey))
}

// A short value gets no hint at all: four characters from each end of a ten character
// secret is most of the secret.
func TestShortSecretsGetNoHint(t *testing.T) {
	require.Empty(t, Hint("short"))
	require.Empty(t, Hint("0123456789"))
	require.NotEmpty(t, Hint("0123456789abcdef"))
}

func TestEncodeAndDecodeSecret(t *testing.T) {
	secret, err := testCipher(t).Encrypt("k.one", apiKey)
	require.NoError(t, err)

	encoded, err := EncodeSecret(secret)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), apiKey)

	decoded, err := DecodeSecret(encoded)
	require.NoError(t, err)
	require.Equal(t, secret.Sealed, decoded.Sealed)
	require.Equal(t, secret.Hint, decoded.Hint)
}

// A plain value in a secret column means somebody wrote around the encryption path.
// It must be an error, not a value that gets used.
func TestDecodingAPlainValueFails(t *testing.T) {
	for _, raw := range []string{`"sk-ant-plaintext"`, `{}`, `{"v":1}`, `null`, `{"unknown":1}`} {
		_, err := DecodeSecret(json.RawMessage(raw))
		require.ErrorIs(t, err, ErrNotEncrypted, raw)
	}
}

func TestNewCipherRejectsAWrongLengthKey(t *testing.T) {
	_, err := NewCipher([]byte("too short"))
	require.Error(t, err)
}

// The stored envelope must contain nothing that looks like the plaintext, at any
// nesting depth of the JSON.
func TestStoredEnvelopeLeaksNothing(t *testing.T) {
	secret, err := testCipher(t).Encrypt("k.one", apiKey)
	require.NoError(t, err)

	encoded, err := EncodeSecret(secret)
	require.NoError(t, err)

	text := string(encoded)
	for _, fragment := range []string{apiKey, "DO-NOT-LOG-ME", "api03"} {
		require.False(t, strings.Contains(text, fragment), fragment)
	}
}
