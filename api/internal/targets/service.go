package targets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// Settings is the slice of the settings service this package needs.
type Settings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	StringList(ctx context.Context, key string, target settings.Target) ([]string, error)
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
}

// Resolver looks up a hostname. An interface so the check can be exercised without
// DNS, and so a runner host with a split-horizon resolver is a deployment detail
// rather than a code change.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Service checks targets.
type Service struct {
	settings Settings
	resolver Resolver

	// lookupTimeout bounds the resolve. A target whose DNS hangs must fail the
	// check rather than hold the request open.
	lookupTimeout time.Duration
}

func NewService(config Settings, resolver Resolver) *Service {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Service{settings: config, resolver: resolver, lookupTimeout: 5 * time.Second}
}

// Target is an approved destination.
//
// It carries the addresses, not just the host, because everything downstream needs
// the address the platform actually checked: the runner pins the name to it, the
// firewall accepts it, and the dialer refuses anything else.
type Target struct {
	// URL as the run will use it, with the scheme and any path preserved.
	URL string

	Host string
	Port string

	// Addresses are every address the host resolved to, all of them checked. A host
	// with one public and one private address is refused rather than partially
	// allowed: which one a connection lands on is not something this platform gets
	// to choose.
	Addresses []netip.Addr

	// AllowPrivate records what the project was permitted at check time, so a later
	// dial can apply the same rule rather than re-reading settings on a hot path.
	AllowPrivate bool
}

// Primary is the address to pin the hostname to.
func (t Target) Primary() netip.Addr {
	if len(t.Addresses) == 0 {
		return netip.Addr{}
	}
	return t.Addresses[0]
}

// HostPort is what a dialer connects to.
func (t Target) HostPort() string { return net.JoinHostPort(t.Host, t.Port) }

// Check resolves the project's configured target and approves it, or refuses with a
// stated reason.
//
// Called before enqueue. Every rejection returns a domain error with a code the UI
// can act on, because "the run failed" is not an answer a user can fix.
func (s *Service) Check(ctx context.Context, projectID uuid.UUID) (Target, error) {
	scope := settings.Target{ProjectID: &projectID}

	base, err := s.settings.String(ctx, "targets.base_url", scope)
	if err != nil {
		return Target{}, apierr.Internal(fmt.Errorf("read the target base URL: %w", err))
	}
	if strings.TrimSpace(base) == "" {
		// Not an error in the platform: a project without a target is a project that
		// has not been pointed at an environment yet (BE-2.13).
		return Target{}, apierr.NoTargetConfigured()
	}

	return s.CheckURL(ctx, projectID, base)
}

// CheckURL approves one explicit URL against the project's allowlist.
//
// Separate from Check because a run may override the target, and an override is
// exactly the input that must not be trusted.
func (s *Service) CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (Target, error) {
	scope := settings.Target{ProjectID: &projectID}

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return Target{}, apierr.Validation(
			"That target is not a URL the platform can use. Expected something like https://staging.example.com.",
			map[string]any{"target": raw})
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Target{}, apierr.Validation(
			fmt.Sprintf("Target scheme %q is not supported. Use http or https.", parsed.Scheme),
			map[string]any{"scheme": parsed.Scheme})
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[parsed.Scheme]
	}

	allowed, err := s.allowlist(ctx, projectID)
	if err != nil {
		return Target{}, err
	}
	if !hostAllowed(host, allowed) {
		// The base URL's own host is allowed automatically, so reaching here means
		// the host was neither the configured target nor on the list.
		return Target{}, apierr.TargetHostNotAllowed(host)
	}

	allowPrivate, err := s.settings.Bool(ctx, "runner.allow_private_targets", scope)
	if err != nil {
		return Target{}, apierr.Internal(fmt.Errorf("read the private-target policy: %w", err))
	}

	addresses, err := s.resolve(ctx, host)
	if err != nil {
		return Target{}, err
	}

	for _, address := range addresses {
		if reason := addressReason(address, allowPrivate); reason != "" {
			return Target{}, apierr.TargetAddressNotAllowed(host, address.String(), reason)
		}
	}

	return Target{
		URL:          parsed.String(),
		Host:         host,
		Port:         port,
		Addresses:    addresses,
		AllowPrivate: allowPrivate,
	}, nil
}

// allowlist is the project's allowed hosts plus the host of its own base URL.
//
// The base URL is included implicitly because requiring an operator to list it
// twice is the kind of duplication that ends with someone widening the allowlist to
// make a run work.
func (s *Service) allowlist(ctx context.Context, projectID uuid.UUID) ([]string, error) {
	scope := settings.Target{ProjectID: &projectID}

	list, err := s.settings.StringList(ctx, "targets.allowlist", scope)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the target allowlist: %w", err))
	}

	base, err := s.settings.String(ctx, "targets.base_url", scope)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the target base URL: %w", err))
	}
	if parsed, parseErr := url.Parse(strings.TrimSpace(base)); parseErr == nil && parsed.Hostname() != "" {
		list = append(list, parsed.Hostname())
	}

	return list, nil
}

// hostAllowed compares hosts case-insensitively and supports one wildcard form,
// a leading "*." matching exactly one level of subdomain.
//
// One level, not any: "*.example.com" allowing "a.b.internal.example.com" is how an
// allowlist stops meaning anything.
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(candidate, ".")))
		if candidate == "" {
			continue
		}
		if candidate == host {
			return true
		}
		if suffix, found := strings.CutPrefix(candidate, "*."); found {
			remainder, matched := strings.CutSuffix(host, "."+suffix)
			if matched && remainder != "" && !strings.Contains(remainder, ".") {
				return true
			}
		}
	}
	return false
}

// resolve looks the host up, accepting a literal address as itself.
func (s *Service) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{literal}, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
	defer cancel()

	addresses, err := s.resolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return nil, apierr.TargetUnresolvable(host, err)
	}
	if len(addresses) == 0 {
		return nil, apierr.TargetUnresolvable(host, errors.New("the host resolved to no addresses"))
	}

	// Unmapped, so an IPv4-in-IPv6 address is judged as the IPv4 address it is:
	// ::ffff:169.254.169.254 must not pass a check that 169.254.169.254 fails.
	for index, address := range addresses {
		addresses[index] = address.Unmap()
	}
	return addresses, nil
}

// addressReason names why an address is refused, or returns empty when it is fine.
//
// The list is deliberately broad. Everything here is either a way to reach the host
// the runner sits on, a way to reach the network it sits in, or a way to reach a
// cloud provider's credential service, and a test target is none of those.
func addressReason(address netip.Addr, allowPrivate bool) string {
	switch {
	case !address.IsValid():
		return "the address is not valid"
	case address.IsUnspecified():
		return "0.0.0.0 and :: are not routable targets"
	case address.IsLoopback():
		return "loopback addresses point at the runner itself"
	case address.IsLinkLocalUnicast(), address.IsLinkLocalMulticast():
		return "link-local addresses include the cloud metadata endpoint"
	case address.IsMulticast():
		return "multicast is not a test target"
	case address.IsInterfaceLocalMulticast():
		return "interface-local multicast is not a test target"
	case isCloudMetadata(address):
		return "this is a cloud instance metadata endpoint"
	case address.IsPrivate() && !allowPrivate:
		return "private addresses are off unless the project opts in for a local staging target"
	case isReservedRange(address) && !allowPrivate:
		return "the address is in a reserved range"
	default:
		return ""
	}
}

// cloudMetadata is every address a provider serves instance credentials from that
// is not already covered by link-local. The runner's firewall drops these too; this
// is the same rule stated where a human reads it.
var cloudMetadata = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"), // AWS, GCP, Azure, and most others.
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud.
	netip.MustParseAddr("192.0.0.192"),     // Oracle Cloud.
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6.
}

func isCloudMetadata(address netip.Addr) bool {
	return slices.ContainsFunc(cloudMetadata, func(known netip.Addr) bool {
		return known.Compare(address) == 0
	})
}

// reservedRanges are ranges that are neither private nor link-local but still are
// not somewhere a test target lives: carrier NAT, benchmarking, documentation, and
// the rest of the IANA special-purpose list that matters here.
var reservedRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // Carrier-grade NAT.
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments.
	netip.MustParsePrefix("192.0.2.0/24"),    // Documentation.
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmarking.
	netip.MustParsePrefix("198.51.100.0/24"), // Documentation.
	netip.MustParsePrefix("203.0.113.0/24"),  // Documentation.
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved.
	netip.MustParsePrefix("fc00::/7"),        // IPv6 unique local.
	netip.MustParsePrefix("2001:db8::/32"),   // IPv6 documentation.
}

func isReservedRange(address netip.Addr) bool {
	return slices.ContainsFunc(reservedRanges, func(prefix netip.Prefix) bool {
		return prefix.Contains(address)
	})
}
