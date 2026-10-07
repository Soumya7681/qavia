package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testParams keep the tests fast. Production cost comes from DefaultHashParams,
// which is deliberately slow.
var testParams = HashParams{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestHashAndVerify(t *testing.T) {
	const password = "correct-horse-battery-staple"

	encoded, err := HashPassword(password, testParams)
	require.NoError(t, err)

	matches, err := VerifyPassword(password, encoded)
	require.NoError(t, err)
	require.True(t, matches)

	matches, err = VerifyPassword("wrong-horse-battery-staple", encoded)
	require.NoError(t, err)
	require.False(t, matches)
}

// The salt is random, so the same password hashes differently every time. Equal
// hashes for equal passwords would let anyone spot shared passwords from the table.
func TestHashesAreSalted(t *testing.T) {
	first, err := HashPassword("same-password-twice", testParams)
	require.NoError(t, err)
	second, err := HashPassword("same-password-twice", testParams)
	require.NoError(t, err)

	require.NotEqual(t, first, second)
}

// The encoded form carries its parameters, so raising the cost later does not
// invalidate existing hashes.
func TestEncodedHashCarriesItsParameters(t *testing.T) {
	encoded, err := HashPassword("passphrase-for-parameters", testParams)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=8192,t=1,p=1$"), encoded)

	// Verification uses the stored parameters, not the current policy.
	matches, err := VerifyPassword("passphrase-for-parameters", encoded)
	require.NoError(t, err)
	require.True(t, matches)
}

func TestNeedsRehashDetectsWeakerParameters(t *testing.T) {
	weak, err := HashPassword("passphrase-for-rehash", testParams)
	require.NoError(t, err)

	require.True(t, NeedsRehash(weak, DefaultHashParams),
		"a hash created with less memory should be upgraded on next login")
	require.False(t, NeedsRehash(weak, testParams))

	strong, err := HashPassword("passphrase-for-rehash", DefaultHashParams)
	require.NoError(t, err)
	require.False(t, NeedsRehash(strong, DefaultHashParams))
}

// A malformed stored hash is a data problem, not a wrong password, and the two must
// never be conflated: treating it as a mismatch would silently lock a user out with
// a misleading message.
func TestMalformedHashIsAnError(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty":          "",
		"not a hash":     "hunter2",
		"wrong scheme":   "$bcrypt$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"wrong version":  "$argon2id$v=13$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"missing fields": "$argon2id$v=19$m=8192,t=1$c2FsdA$aGFzaA",
		"bad base64":     "$argon2id$v=19$m=8192,t=1,p=1$!!!$aGFzaA",
	} {
		t.Run(name, func(t *testing.T) {
			matches, err := VerifyPassword("anything", encoded)
			require.ErrorIs(t, err, ErrInvalidHash)
			require.False(t, matches)
		})
	}
}

// The decoy is what keeps an unknown account costing the same as a wrong password.
// If it stops parsing, the timing difference comes back silently.
func TestDecoyHashIsValid(t *testing.T) {
	matches, err := VerifyPassword("anything at all", decoyHash)
	require.NoError(t, err, "decoyHash must parse, or unknown accounts respond faster than real ones")
	require.False(t, matches)
}

func TestValidatePassword(t *testing.T) {
	reason, ok := ValidatePassword("")
	require.False(t, ok)
	require.Contains(t, reason, "Enter a password")

	reason, ok = ValidatePassword("short")
	require.False(t, ok)
	require.Contains(t, reason, "at least 12 characters")

	reason, ok = ValidatePassword(strings.Repeat("x", MaxPasswordLength+1))
	require.False(t, ok)
	require.Contains(t, reason, "at most")

	for _, accepted := range []string{"twelvechars!", "a passphrase of several words"} {
		reason, ok = ValidatePassword(accepted)
		require.True(t, ok, accepted)
		require.Empty(t, reason)
	}
}

// Length is counted in runes, so a short password of multi-byte characters is not
// accepted just because its byte length passes.
func TestValidatePasswordCountsRunesNotBytes(t *testing.T) {
	// Eight characters, well over twelve bytes.
	reason, ok := ValidatePassword("पासवर्ड१")
	require.False(t, ok)
	require.Contains(t, reason, "at least 12 characters")
}
