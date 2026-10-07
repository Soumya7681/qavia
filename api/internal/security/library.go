package security

import "sort"

// The curated payload library (BE-9.2, F-11.3).
//
// Seven categories, each a small set of payloads chosen to exercise one class of
// mistake rather than to be exhaustive: fifty variants of the same SQL fragment test
// the same code path fifty times. Every payload states what it is for, because a
// finding that cites "payload sql-boolean-true" has to be explainable to the person who
// has to fix it, and a payload nobody can explain is one nobody should have sent.
//
// This is data, deliberately. Adding a payload is a reviewed change to a constant, and
// the review is the control: a payload here is a request this platform will send at a
// client's server, and that decision belongs to a person reading a diff, never to a
// model choosing a string at run time (BE-9.2.3).

// Category groups payloads by the weakness they probe.
type Category string

const (
	CategorySQLi       Category = "sqli"
	CategoryXSS        Category = "xss"
	CategoryCSRF       Category = "csrf"
	CategoryJWT        Category = "jwt"
	CategoryIDOR       Category = "idor"
	CategoryRateLimit  Category = "rate_limit"
	CategoryBrokenAuth Category = "broken_auth"
)

// Categories is the fixed set, in a stable order, so a settings screen and a probe plan
// list them the same way every time.
func Categories() []Category {
	return []Category{
		CategorySQLi, CategoryXSS, CategoryCSRF, CategoryJWT,
		CategoryIDOR, CategoryRateLimit, CategoryBrokenAuth,
	}
}

// Delivery says where a payload goes and how the response is judged, because "did this
// work" is category-specific: a reflected XSS payload is found when it comes back
// unescaped, and a broken-auth probe is found when a request with no credential
// succeeds.
type Delivery string

const (
	// DeliveryParameter substitutes the payload into a request parameter or body
	// field.
	DeliveryParameter Delivery = "parameter"

	// DeliveryHeader sets or replaces a request header, for the JWT and some auth
	// probes.
	DeliveryHeader Delivery = "header"

	// DeliveryOmitAuth sends the request with its credential removed, for broken-auth.
	DeliveryOmitAuth Delivery = "omit_auth"

	// DeliveryRepeat sends the same request many times, for rate-limit.
	DeliveryRepeat Delivery = "repeat"

	// DeliveryIdentifier replaces an identifier in the path with one belonging to
	// somebody else, for IDOR.
	DeliveryIdentifier Delivery = "identifier"
)

// Detect names how a probe's result is judged. Each is a rule the executor applies to
// the response, never a model's opinion.
type Detect string

const (
	// DetectReflected: the payload appears in the response unescaped (XSS).
	DetectReflected Detect = "reflected"

	// DetectError: the response carries a database or stack error the payload provoked
	// (SQLi).
	DetectError Detect = "error"

	// DetectStatusSuccess: a request that should have been refused returned 2xx
	// (broken auth, IDOR, CSRF).
	DetectStatusSuccess Detect = "status_success"

	// DetectNotRateLimited: a burst never drew a 429 (rate limit).
	DetectNotRateLimited Detect = "not_rate_limited"

	// DetectAccepted: a tampered token was accepted (JWT).
	DetectAccepted Detect = "accepted"
)

// Payload is one reviewed probe.
type Payload struct {
	// ID is stable and is what a probe plan names, so a plan carries "sqli-boolean" and
	// never the string it stands for (BE-9.3.3).
	ID       string
	Category Category

	// Value is the payload sent. Empty for the deliveries that need no string — an
	// omitted credential, a burst of the unmodified request.
	Value string

	Delivery Delivery
	Detect   Detect

	// Purpose is the sentence a finding cites. Required: a payload without one is a
	// payload nobody reviewed the reason for (BE-9.2.3).
	Purpose string

	// Severity is what a finding starts at when this payload succeeds. A reviewer can
	// change a finding's severity, but the default is a property of the weakness.
	Severity Severity
}

// Severity is a finding's seriousness.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// library is the whole of what this platform will ever send. Nothing is generated; the
// entries are constants, so adding one is a diff a person reads.
var library = []Payload{
	// --- SQL injection. Boolean and error-based, which between them cover the common
	// forms without a scanner's noise.
	{
		ID: "sqli-boolean-true", Category: CategorySQLi, Value: "' OR '1'='1",
		Delivery: DeliveryParameter, Detect: DetectError,
		Purpose:  "A boolean-true injection: if the query changes shape, the response differs from the same request with a false variant, or a database error leaks.",
		Severity: SeverityCritical,
	},
	{
		ID: "sqli-boolean-false", Category: CategorySQLi, Value: "' OR '1'='2",
		Delivery: DeliveryParameter, Detect: DetectError,
		Purpose:  "The false half of the boolean pair: compared against the true one, a difference in the response is evidence the input reaches the query.",
		Severity: SeverityCritical,
	},
	{
		ID: "sqli-error-quote", Category: CategorySQLi, Value: "'",
		Delivery: DeliveryParameter, Detect: DetectError,
		Purpose:  "A bare apostrophe: an unparameterised query answers it with a syntax error naming the database, which is the plainest evidence of injection.",
		Severity: SeverityCritical,
	},
	{
		ID: "sqli-union", Category: CategorySQLi, Value: "' UNION SELECT NULL--",
		Delivery: DeliveryParameter, Detect: DetectError,
		Purpose:  "A union probe: a column-count mismatch provokes a database error, and a match begins to leak other tables.",
		Severity: SeverityCritical,
	},

	// --- Cross-site scripting. Reflected, judged by whether the markup comes back
	// unescaped.
	{
		ID: "xss-script-tag", Category: CategoryXSS, Value: "<script>qaviaXSS(1)</script>",
		Delivery: DeliveryParameter, Detect: DetectReflected,
		Purpose:  "A script tag with a unique marker: returned in an HTML context without escaping, it would execute, and the marker makes the reflection unambiguous.",
		Severity: SeverityHigh,
	},
	{
		ID: "xss-img-onerror", Category: CategoryXSS, Value: "<img src=x onerror=qaviaXSS(2)>",
		Delivery: DeliveryParameter, Detect: DetectReflected,
		Purpose:  "An event-handler payload for filters that strip <script> but not attributes.",
		Severity: SeverityHigh,
	},
	{
		ID: "xss-attribute-break", Category: CategoryXSS, Value: "\"><svg onload=qaviaXSS(3)>",
		Delivery: DeliveryParameter, Detect: DetectReflected,
		Purpose:  "An attribute-context breakout: reflected inside a tag attribute, the closing quote and bracket escape into markup.",
		Severity: SeverityHigh,
	},

	// --- CSRF. A state-changing request made with no anti-CSRF token succeeding is the
	// finding.
	{
		ID: "csrf-no-token", Category: CategoryCSRF, Delivery: DeliveryParameter, Detect: DetectStatusSuccess,
		Purpose:  "A state-changing request sent without any anti-CSRF token: if it succeeds, the endpoint has no CSRF protection worth the name.",
		Severity: SeverityMedium,
	},

	// --- JWT tampering. The token is altered in ways a correct verifier rejects.
	{
		ID: "jwt-alg-none", Category: CategoryJWT, Delivery: DeliveryHeader, Detect: DetectAccepted,
		Purpose:  "A token re-signed with alg:none: accepted, it means the server trusts the token's own claim about how it was signed.",
		Severity: SeverityCritical,
	},
	{
		ID: "jwt-signature-strip", Category: CategoryJWT, Delivery: DeliveryHeader, Detect: DetectAccepted,
		Purpose:  "The same token with its signature removed: accepted, the server is not verifying signatures at all.",
		Severity: SeverityCritical,
	},
	{
		ID: "jwt-expired", Category: CategoryJWT, Delivery: DeliveryHeader, Detect: DetectAccepted,
		Purpose:  "A token with an expiry in the past: accepted, expiry is not being checked.",
		Severity: SeverityHigh,
	},

	// --- IDOR. An identifier is swapped for one the caller should not be able to read.
	{
		ID: "idor-adjacent-id", Category: CategoryIDOR, Delivery: DeliveryIdentifier, Detect: DetectStatusSuccess,
		Purpose:  "A neighbouring resource identifier substituted into the path: a 2xx means the server returned somebody else's record without an ownership check.",
		Severity: SeverityHigh,
	},

	// --- Rate limiting. A burst that never draws a 429.
	{
		ID: "rate-limit-burst", Category: CategoryRateLimit, Delivery: DeliveryRepeat, Detect: DetectNotRateLimited,
		Purpose:  "A short burst of identical requests: none refused with 429 means an endpoint an attacker can hammer — credential stuffing, enumeration, denial of service.",
		Severity: SeverityMedium,
	},

	// --- Broken authentication. The credential is removed from a request that needs
	// one.
	{
		ID: "broken-auth-no-credential", Category: CategoryBrokenAuth, Delivery: DeliveryOmitAuth, Detect: DetectStatusSuccess,
		Purpose:  "A protected request sent with no credential at all: a 2xx means the endpoint's authentication is optional in practice however it is documented.",
		Severity: SeverityCritical,
	},
	{
		ID: "broken-auth-empty-bearer", Category: CategoryBrokenAuth, Value: "Bearer ", Delivery: DeliveryHeader, Detect: DetectStatusSuccess,
		Purpose:  "An empty bearer token: accepted, the server treats the presence of the header rather than its contents as authentication.",
		Severity: SeverityHigh,
	},
}

// byID indexes the library for lookup during planning and execution.
var byID = func() map[string]Payload {
	index := make(map[string]Payload, len(library))
	for _, payload := range library {
		index[payload.ID] = payload
	}
	return index
}()

// Lookup returns a payload by ID, and whether it exists. Used when a probe plan is
// validated: a plan naming a payload the library does not have is refused, which is the
// enforcement that a plan can only ever reference reviewed entries (BE-9.3.2).
func Lookup(id string) (Payload, bool) {
	payload, ok := byID[id]
	return payload, ok
}

// All returns the whole library, category then ID, so the order is stable.
func All() []Payload {
	out := make([]Payload, len(library))
	copy(out, library)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ForCategory returns one category's payloads.
func ForCategory(category Category) []Payload {
	var out []Payload
	for _, payload := range library {
		if payload.Category == category {
			out = append(out, payload)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Catalogue is the library described for a probe-selection agent: the IDs and purposes,
// and never the payload values. The agent chooses from this, which is what keeps the
// strings out of its context entirely (BE-9.3.2).
type CatalogueEntry struct {
	ID       string
	Category Category
	Purpose  string
	Delivery Delivery
	Severity Severity
}

// Catalogue lists the library without its payload strings, for handing to the agent.
func Catalogue() []CatalogueEntry {
	entries := make([]CatalogueEntry, 0, len(library))
	for _, payload := range All() {
		entries = append(entries, CatalogueEntry{
			ID:       payload.ID,
			Category: payload.Category,
			Purpose:  payload.Purpose,
			Delivery: payload.Delivery,
			Severity: payload.Severity,
		})
	}
	return entries
}

// ValidCategory reports whether a string is one of the seven.
func ValidCategory(category string) bool {
	for _, candidate := range Categories() {
		if Category(category) == candidate {
			return true
		}
	}
	return false
}
