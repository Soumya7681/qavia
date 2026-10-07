package targets

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Dial-time enforcement (BE-4.7.3).
//
// Checking a host, then connecting to it, leaves a window: DNS can answer
// differently the second time, and the second time is the one that matters. A
// rebinding record with a one-second TTL returns a public address to the check and a
// private one to the dial, and a resolve-then-dial platform connects to the private
// one.
//
// The Control hook closes that window. It runs after the resolver and before the
// connect, with the address the kernel is about to use, so there is no second lookup
// left to race.

// SafeDialer returns a dialer that refuses any address a target check would have
// refused.
//
// It is not tied to one target: the rule it enforces is the address policy, so the
// same dialer protects a webhook delivery, an integration callback, and a target
// health probe. Pass allowPrivate only for a project that opted into a private
// staging target.
func SafeDialer(allowPrivate bool) *net.Dialer {
	return &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   controlFor(nil, allowPrivate),
	}
}

// PinnedDialer refuses anything except the addresses a check already approved.
//
// Stricter than SafeDialer and used where the destination is known in advance: an
// approved target's addresses are the whole allowed set, so a rebinding record
// pointing anywhere else, public or not, is refused.
func PinnedDialer(target Target) *net.Dialer {
	return &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   controlFor(target.Addresses, target.AllowPrivate),
	}
}

// controlFor builds the hook. Signature is fixed by net.Dialer: the address arrives
// as a string because it is what the syscall layer has, and a parse failure is
// refused rather than assumed safe.
func controlFor(pinned []netip.Addr, allowPrivate bool) func(string, string, syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		switch network {
		case "tcp", "tcp4", "tcp6":
		default:
			return fmt.Errorf("network %q is not permitted for an outbound connection", network)
		}

		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("refusing a connection to %q: %w", address, err)
		}

		parsed, err := netip.ParseAddr(host)
		if err != nil {
			// The hook is called with a literal address; anything else means the
			// resolver returned something unexpected, and unexpected is refused.
			return fmt.Errorf("refusing a connection to %q: not an address", host)
		}
		parsed = parsed.Unmap()

		if len(pinned) > 0 {
			for _, allowed := range pinned {
				if allowed.Unmap().Compare(parsed) == 0 {
					return nil
				}
			}
			return &DialRefusedError{
				Address: parsed.String(),
				Reason:  "the address is not one the target check approved, which is what a rebinding DNS record looks like",
			}
		}

		if reason := addressReason(parsed, allowPrivate); reason != "" {
			return &DialRefusedError{Address: parsed.String(), Reason: reason}
		}
		return nil
	}
}

// DialRefusedError is returned through net.Dialer, so it surfaces wrapped inside a
// url.Error. It is its own type so a caller can tell "we refused this" from "the
// network failed", which are different things to tell a user.
type DialRefusedError struct {
	Address string
	Reason  string
}

func (e *DialRefusedError) Error() string {
	return fmt.Sprintf("refused to connect to %s: %s", e.Address, e.Reason)
}

// HTTPClient is an outbound client with the guard installed.
//
// Redirects are the reason this exists as a helper rather than as advice: a target
// that answers with a 302 to http://169.254.169.254 would otherwise be followed by
// a client that never re-checked anything. The transport's dialer checks every hop,
// and the redirect count is capped.
func HTTPClient(allowPrivate bool, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext:           SafeDialer(allowPrivate).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     false,
		MaxIdleConns:          8,
		Proxy:                 nil, // No proxy: a proxy would dial for us and skip the hook.
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			if scheme := request.URL.Scheme; scheme != "http" && scheme != "https" {
				return fmt.Errorf("refusing a redirect to scheme %q", scheme)
			}
			return nil
		},
	}
}

// Probe checks the target answers before a run is enqueued.
//
// Cheap, and it turns "every test failed with a connection error" into "the target
// did not answer", which is the difference between a user debugging their suite and
// a user fixing their environment.
func (s *Service) Probe(ctx context.Context, target Target) error {
	dialer := PinnedDialer(target)

	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Primary().String(), target.Port))
	if err != nil {
		return err
	}
	return connection.Close()
}
