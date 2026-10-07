package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// Credentials are the secret half of a provider, per kind
// (ai-architecture.md 3.3).
//
// They are stored as a jsonb object of individually encrypted values rather than
// one encrypted blob. Encrypting per field means a hint can be shown for the field
// a user is about to replace, and rotating the key re-seals each value with the
// same machinery the settings service already uses.
type Credentials map[string]string

// requiredFields per kind. Optional fields are absent on purpose: an adapter
// tolerates their absence, and demanding them would refuse working setups.
var requiredFields = map[Kind][]string{
	KindAnthropic:   {"api_key"},
	KindOpenAI:      {"api_key"},
	KindAzureOpenAI: {"api_key", "endpoint", "deployment", "api_version"},
	KindGemini:      {"api_key"},
	// bedrock is checked by hand below: instance-role mode stores no keys at all,
	// and that is the preferred production path.
	KindBedrock:          {},
	KindVertex:           {"service_account_json", "project_id"},
	KindOpenAICompatible: {"base_url"},
}

// secretFields are the values worth encrypting. Everything else in a credential
// object is addressing rather than authorisation: an endpoint or a region is not a
// secret, and encrypting it only makes it unreadable in a support conversation.
var secretFields = map[string]bool{
	"api_key":              true,
	"secret_access_key":    true,
	"session_token":        true,
	"service_account_json": true,
}

// Validate checks the shape for a kind before anything is stored.
//
// The message names the missing field, because the alternative is a provider 401
// twenty seconds into a job and nobody knowing which field was wrong.
func (c Credentials) Validate(kind Kind) error {
	if kind == KindBedrock {
		return c.validateBedrock()
	}

	var missing []string
	for _, field := range requiredFields[kind] {
		if strings.TrimSpace(c[field]) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return apierr.Validation(
			fmt.Sprintf("A %s provider needs %s.", kind, strings.Join(missing, ", ")),
			map[string]any{"kind": string(kind), "missing": missing})
	}
	return nil
}

// validateBedrock accepts either an instance role or a key pair, and says so.
func (c Credentials) validateBedrock() error {
	if strings.TrimSpace(c["region"]) == "" {
		return apierr.Validation("A bedrock provider needs a region.",
			map[string]any{"kind": string(KindBedrock), "missing": []string{"region"}})
	}
	if strings.EqualFold(strings.TrimSpace(c["use_instance_role"]), "true") {
		// No stored keys at all, and IAM handles it. This is the path to prefer on
		// EC2 or ECS.
		return nil
	}

	var missing []string
	for _, field := range []string{"access_key_id", "secret_access_key"} {
		if strings.TrimSpace(c[field]) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return apierr.Validation(
			"A bedrock provider needs either an access key pair or use_instance_role.",
			map[string]any{"kind": string(KindBedrock), "missing": missing})
	}
	return nil
}

// seal encrypts the secret fields and leaves the rest readable.
func (c Credentials) seal(cipher *settings.Cipher, providerName string) (json.RawMessage, error) {
	sealed := make(map[string]json.RawMessage, len(c))

	for field, value := range c {
		if !secretFields[field] {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("encode credential %q: %w", field, err)
			}
			sealed[field] = encoded
			continue
		}

		// The setting key is part of the sealed envelope's associated data, so a
		// ciphertext moved from one provider to another fails to open rather than
		// silently decrypting.
		secret, err := cipher.Encrypt(credentialKey(providerName, field), value)
		if err != nil {
			return nil, err
		}
		encoded, err := settings.EncodeSecret(secret)
		if err != nil {
			return nil, err
		}
		sealed[field] = encoded
	}

	return json.Marshal(sealed)
}

// open decrypts the secret fields for one call.
//
// The plaintext is returned for immediate use and must not be cached or held: it
// travels to the Python service in the request body and is gone
// (backend-standards.md 6).
func openCredentials(
	cipher *settings.Cipher,
	providerName string,
	raw json.RawMessage,
) (Credentials, error) {
	stored, err := decodeCredentials(raw)
	if err != nil {
		return nil, err
	}

	out := make(Credentials, len(stored))
	for field, value := range stored {
		if !secretFields[field] {
			var plain string
			if err := json.Unmarshal(value, &plain); err != nil {
				return nil, fmt.Errorf("decode credential %q: %w", field, err)
			}
			out[field] = plain
			continue
		}

		secret, err := settings.DecodeSecret(value)
		if err != nil {
			return nil, apierr.SecretNotReadable(field).
				WithCause(fmt.Errorf("stored credential %q is unreadable: %w", field, err))
		}
		plain, err := cipher.Decrypt(credentialKey(providerName, field), secret)
		if err != nil {
			return nil, err
		}
		out[field] = plain
	}
	return out, nil
}

// decodeCredentials reads the stored object without decrypting anything, which is
// all the read path needs: it reports whether a credential is set and a hint, and
// never the value (F-1.8).
func decodeCredentials(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	return stored, nil
}

// hintOf renders the recognisable part of a stored credential.
//
// It reads the hint the sealed envelope already carries rather than decrypting:
// the read path must not be able to produce a plaintext at all, which is a
// stronger guarantee than choosing not to.
func hintOf(stored map[string]json.RawMessage) string {
	fields := make([]string, 0, len(stored))
	for field := range stored {
		if secretFields[field] {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)

	for _, field := range fields {
		secret, err := settings.DecodeSecret(stored[field])
		if err == nil && secret.Hint != "" {
			return secret.Hint
		}
	}
	return ""
}

// credentialKey namespaces one provider's field inside the cipher.
func credentialKey(providerName, field string) string {
	return "ai.provider." + providerName + "." + field
}
