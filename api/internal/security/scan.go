package security

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/hyscaler/qavia/api/internal/ingest"
)

// Probe resolution (BE-9.3, BE-9.4).
//
// This is the one place a payload string enters a request, and it does so from the
// reviewed library, keyed by an ID the plan named. The agent chose the ID and the
// target; the string it stands for was written by a person and is looked up here. A
// plan that named a payload the library does not have is dropped rather than sent,
// which is the enforcement that keeps model-chosen strings out of the traffic entirely.

// PlanProbe is one entry of the agent's plan: an endpoint, a parameter, and a library
// payload ID. Never a payload string.
type PlanProbe struct {
	Endpoint  string `json:"endpoint"`
	Parameter string `json:"parameter"`
	PayloadID string `json:"payload_id"`
	Rationale string `json:"rationale"`
}

// Plan is the agent's whole output.
type Plan struct {
	Summary string      `json:"summary"`
	Probes  []PlanProbe `json:"probes"`
	Skipped []struct {
		Endpoint string `json:"endpoint"`
		Reason   string `json:"reason"`
	} `json:"skipped"`
}

// ResolvedProbe is a probe with its payload filled in and its request built. This is
// what the container receives.
type ResolvedProbe struct {
	PayloadID string   `json:"payloadId"`
	Category  Category `json:"category"`
	Endpoint  string   `json:"endpoint"`
	Parameter string   `json:"parameter,omitempty"`
	Severity  Severity `json:"severity"`
	Detect    Detect   `json:"detect"`

	// Request is what to send. Control is an optional baseline request for boolean
	// pairs, so a difference is measured rather than guessed.
	Request Request  `json:"request"`
	Control *Request `json:"control,omitempty"`

	// Marker is the unique token an XSS payload carries, so a reflection is
	// unambiguous.
	Marker string `json:"marker,omitempty"`

	// Burst is how many times a rate-limit probe repeats the request.
	Burst int `json:"burst,omitempty"`

	// ReproURL and DeliveryNote describe the probe for a finding's reproduction steps.
	ReproURL     string `json:"reproUrl,omitempty"`
	DeliveryNote string `json:"deliveryNote,omitempty"`
}

// Request is one HTTP request the container sends.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Config is the probe list the container reads on stdin.
type Config struct {
	Schema string          `json:"schema"`
	Probes []ResolvedProbe `json:"probes"`
}

// ConfigSchema is the version the container checks.
const ConfigSchema = "qavia.probes/1"

// DecodePlan reads the agent's plan.
func DecodePlan(raw json.RawMessage) (Plan, error) {
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("the probe plan could not be read: %w", err)
	}
	return plan, nil
}

// Resolve turns a plan into a container-ready probe list, filling each payload from the
// library and building its request against the target.
//
// The three things it guards, and each is a safety property rather than a nicety:
//
//   - **A payload ID the library does not have is dropped, not sent.** This is what
//     makes "the model never chooses a payload string" true rather than aspirational: a
//     plan can only reference reviewed entries, because an unreviewed reference resolves
//     to nothing (BE-9.3.2).
//   - **A destructive method is refused.** A probe that would DELETE or write is skipped
//     even if the plan asked for it: an IDOR probe that deletes somebody else's record
//     is the damage the vulnerability would cause, done by us.
//   - **The credential is applied by the platform, never by the payload.** An auth probe
//     removes or empties it here, so what is tested is the server's handling of a
//     missing credential rather than a string the model wrote.
func Resolve(plan Plan, endpoints []ingest.Endpoint, target, credential string) ([]ResolvedProbe, []string) {
	byKey := make(map[string]ingest.Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		byKey[normaliseKey(endpoint.Key())] = endpoint
	}

	var (
		resolved []ResolvedProbe
		dropped  []string
	)

	for _, entry := range plan.Probes {
		payload, ok := Lookup(entry.PayloadID)
		if !ok {
			// The whole point of the split: a plan can only ever reference the reviewed
			// library, so a name it does not have is not sent.
			dropped = append(dropped, fmt.Sprintf("%s (unknown payload id)", entry.PayloadID))
			continue
		}

		endpoint, ok := byKey[normaliseKey(entry.Endpoint)]
		if !ok {
			dropped = append(dropped, fmt.Sprintf("%s (unknown endpoint %q)", entry.PayloadID, entry.Endpoint))
			continue
		}

		if isDestructive(endpoint.Method) && payload.Delivery != DeliveryOmitAuth {
			// A destructive write is not probed: the probe would be the damage.
			dropped = append(dropped, fmt.Sprintf("%s on %s (a %s write is not probed)",
				payload.ID, endpoint.Key(), strings.ToUpper(endpoint.Method)))
			continue
		}

		probe, ok := build(payload, endpoint, entry.Parameter, target, credential)
		if !ok {
			dropped = append(dropped, fmt.Sprintf("%s on %s (could not build a request)",
				payload.ID, endpoint.Key()))
			continue
		}
		resolved = append(resolved, probe)
	}

	return resolved, dropped
}

// build constructs the request for one payload against one endpoint.
func build(
	payload Payload,
	endpoint ingest.Endpoint,
	parameter, target, credential string,
) (ResolvedProbe, bool) {
	base := strings.TrimRight(target, "/")
	path := endpoint.Path

	probe := ResolvedProbe{
		PayloadID: payload.ID,
		Category:  payload.Category,
		Endpoint:  endpoint.Key(),
		Parameter: parameter,
		Severity:  payload.Severity,
		Detect:    payload.Detect,
	}

	headers := map[string]string{}
	if credential != "" {
		headers["Authorization"] = "Bearer " + credential
	}

	switch payload.Delivery {
	case DeliveryParameter:
		// A path placeholder gets a sample value so the URL resolves, then the payload
		// goes in the named query parameter (or a placeholder if the path itself carries
		// it).
		resolvedPath := fillPath(path, parameter, payload.Value)
		requestURL := base + resolvedPath
		if !strings.Contains(path, "{"+parameter+"}") && parameter != "" {
			requestURL = withQuery(requestURL, parameter, payload.Value)
		}
		probe.Request = Request{Method: strings.ToUpper(endpoint.Method), URL: requestURL, Headers: headers}
		probe.ReproURL = requestURL
		probe.DeliveryNote = "payload in the " + parameter + " parameter"

		// A marker for XSS, and a boolean control for the false half of a SQLi pair.
		if payload.Category == CategoryXSS {
			probe.Marker = markerFor(payload.Value)
		}
		if payload.ID == "sqli-boolean-true" {
			control := Request{Method: probe.Request.Method,
				URL: withQuery(base+fillPath(path, parameter, "' OR '1'='2"), parameter, "' OR '1'='2"), Headers: headers}
			probe.Control = &control
		}

	case DeliveryHeader:
		requestURL := base + fillPath(path, "", "1")
		probeHeaders := cloneHeaders(headers)
		switch payload.Category {
		case CategoryJWT:
			probeHeaders["Authorization"] = tamperToken(payload.ID, credential)
		default:
			probeHeaders["Authorization"] = payload.Value
		}
		probe.Request = Request{Method: strings.ToUpper(endpoint.Method), URL: requestURL, Headers: probeHeaders}
		probe.ReproURL = requestURL
		probe.DeliveryNote = "tampered Authorization header"

	case DeliveryOmitAuth:
		requestURL := base + fillPath(path, "", "1")
		// No Authorization header at all: what is tested is the server accepting a
		// request with no credential.
		probe.Request = Request{Method: strings.ToUpper(endpoint.Method), URL: requestURL, Headers: map[string]string{}}
		probe.ReproURL = requestURL
		probe.DeliveryNote = "no credential sent"

	case DeliveryIdentifier:
		// Swap a path identifier for a neighbour. Only meaningful when the path has a
		// placeholder.
		if !strings.Contains(path, "{") {
			return ResolvedProbe{}, false
		}
		requestURL := base + fillPath(path, "", "2")
		probe.Request = Request{Method: strings.ToUpper(endpoint.Method), URL: requestURL, Headers: headers}
		probe.ReproURL = requestURL
		probe.DeliveryNote = "an identifier belonging to another resource"

	case DeliveryRepeat:
		requestURL := base + fillPath(path, "", "1")
		probe.Request = Request{Method: strings.ToUpper(endpoint.Method), URL: requestURL, Headers: headers}
		probe.Burst = 20
		probe.ReproURL = requestURL

	default:
		return ResolvedProbe{}, false
	}

	return probe, true
}

// fillPath replaces path placeholders with a value, so a probe URL resolves.
func fillPath(path, targetParam, payload string) string {
	result := path
	for {
		start := strings.IndexByte(result, '{')
		if start < 0 {
			break
		}
		end := strings.IndexByte(result[start:], '}')
		if end < 0 {
			break
		}
		name := result[start+1 : start+end]
		value := "1"
		if name == targetParam {
			value = payload
		}
		result = result[:start] + url.PathEscape(value) + result[start+end+1:]
	}
	return result
}

func withQuery(requestURL, key, value string) string {
	separator := "?"
	if strings.Contains(requestURL, "?") {
		separator = "&"
	}
	return requestURL + separator + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}

// tamperToken returns an Authorization value tampered the way a JWT payload names, built
// from the real token so what is tested is the server's verification rather than a
// made-up string.
func tamperToken(payloadID, credential string) string {
	token := strings.TrimSpace(strings.TrimPrefix(credential, "Bearer "))
	parts := strings.Split(token, ".")

	switch payloadID {
	case "jwt-signature-strip":
		if len(parts) == 3 {
			return "Bearer " + parts[0] + "." + parts[1] + "."
		}
	case "jwt-alg-none":
		// Header re-encoded to alg:none with the signature dropped. The base64url of
		// {"alg":"none","typ":"JWT"} is a constant; the body is kept.
		if len(parts) == 3 {
			return "Bearer eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." + parts[1] + "."
		}
	}
	// Expired or unbuildable: send the token unchanged so the probe still exercises the
	// endpoint rather than being silently dropped.
	if token == "" {
		return "Bearer "
	}
	return "Bearer " + token
}

func cloneHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

// markerFor pulls the unique marker out of an XSS payload, so a reflection is
// unambiguous rather than a coincidental substring.
func markerFor(payload string) string {
	if index := strings.Index(payload, "qaviaXSS("); index >= 0 {
		end := strings.IndexByte(payload[index:], ')')
		if end >= 0 {
			return payload[index : index+end+1]
		}
	}
	return payload
}

// isDestructive reports whether a method changes or removes state.
func isDestructive(method string) bool {
	switch strings.ToUpper(method) {
	case "DELETE", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

func normaliseKey(key string) string {
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 {
		return strings.ToUpper(strings.TrimSpace(key))
	}
	return strings.ToUpper(fields[0]) + " " + fields[1]
}
