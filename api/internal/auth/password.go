package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// HashParams are the Argon2id cost parameters.
//
// They live in the settings registry (BE-0.13) so they can be raised without a
// deploy as hardware improves. DefaultHashParams is the code default the registry
// declares, and it is what runs until a value is stored.
type HashParams struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultHashParams follows the OWASP guidance for Argon2id: 19 MiB, two
// iterations, one degree of parallelism.
//
// The cost is deliberately noticeable. Login is a rare operation and a fast hash
// is the whole attack.
var DefaultHashParams = HashParams{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// MinPasswordLength is the floor. Length beats composition rules: a 12-character
// passphrase is stronger than "P@ssw0rd" and easier to remember, so there are no
// character-class requirements.
const MinPasswordLength = 12

// MaxPasswordLength bounds the work an unauthenticated caller can ask for. Argon2
// cost is independent of input length, but an unbounded body is still an
// unbounded read.
const MaxPasswordLength = 1024

// ErrInvalidHash means a stored hash is not in the expected encoding. It is a data
// problem, never a wrong password, and the two must not be conflated.
var ErrInvalidHash = errors.New("stored password hash is malformed")

// HashPassword produces an encoded Argon2id hash.
//
// The encoded form carries its own parameters, so raising the cost later does not
// invalidate existing hashes: an old hash still verifies with the parameters it
// was created under, and can be upgraded on next login.
func HashPassword(password string, params HashParams) (string, error) {
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether the password matches the encoded hash.
//
// The comparison is constant time. A byte-by-byte comparison leaks how much of the
// hash matched, which is enough to reconstruct it.
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a stored hash was created with weaker parameters
// than the current policy, so it can be upgraded during a successful login.
func NeedsRehash(encoded string, current HashParams) bool {
	stored, _, _, err := decodeHash(encoded)
	if err != nil {
		// Unreadable is worse than weak. Rehashing replaces it.
		return true
	}
	return stored.Memory < current.Memory ||
		stored.Iterations < current.Iterations ||
		stored.KeyLength < current.KeyLength
}

// ValidatePassword checks the policy and returns the reason it failed.
//
// It returns a message rather than an error on purpose. This is user-facing copy: a
// sentence, capitalised, naming the rule that failed, because "invalid password"
// tells a user nothing. Go error strings are lowercase and unpunctuated, and
// pretending this is one would put the two conventions in conflict.
func ValidatePassword(password string) (reason string, ok bool) {
	length := utf8.RuneCountInString(password)

	switch {
	case length == 0:
		return "Enter a password.", false
	case length < MinPasswordLength:
		return fmt.Sprintf(
			"Use at least %d characters. A short passphrase of several words is fine.",
			MinPasswordLength), false
	case len(password) > MaxPasswordLength:
		return fmt.Sprintf("Use at most %d characters.", MaxPasswordLength), false
	default:
		return "", true
	}
}

func decodeHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	var params HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))
	return params, salt, key, nil
}
