package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Egress control (BE-4.4).
//
// Docker's own network modes give two useful states and no third: `none` means no
// route at all, and a bridge means the whole internet. A run with an allowlisted
// target needs neither, so the platform builds the third state itself:
//
//  1. A network per run, so one run's rules can never widen another's.
//  2. Filter rules in the host's firewall, attached to that network's bridge
//     interface, that accept the allowlisted addresses and drop everything else.
//  3. `ExtraHosts` pinning each allowlisted name to the address the platform
//     already checked, so no resolver is needed inside the container and no second
//     lookup can move the target.
//
// The rules are installed by a short-lived helper container in the host network
// namespace with NET_ADMIN. That is not an escalation: this process already talks
// to the Docker socket, which is root-equivalent on the host. It is the mechanism
// that lets the worker program the host firewall without the worker itself being
// root, and it keeps the capability in a container that exists for two seconds
// rather than in the worker for its whole life.
//
// The run container itself never gets NET_ADMIN, never gets NET_RAW, and cannot see
// or change these rules.

// netChainPrefix names the per-run firewall chain. Every rule the platform installs
// lives in a chain with this prefix, which is what makes removal exact: the sweeper
// deletes chains it can prove are ours and touches nothing else.
const netChainPrefix = "QAVIA-"

// networkName is the per-run Docker network.
func networkName(runID string) string { return containerPrefix + strings.ToLower(runID) + "-net" }

// chainName is the per-run firewall chain.
//
// iptables caps a chain name at 28 characters, so the run ID is truncated. A
// collision would mean two live runs sharing a chain, and the chain only ever
// contains accept rules for the addresses one of them was allowed to reach, so the
// failure mode is bounded: it cannot widen access beyond the union of two runs the
// same project already started. The full run ID stays on the network's label for
// attribution.
func chainName(runID string) string {
	compact := strings.ReplaceAll(strings.ToLower(runID), "-", "")
	if len(compact) > 20 {
		compact = compact[:20]
	}
	return netChainPrefix + strings.ToUpper(compact)
}

// runNetwork is what a prepared network looks like to the driver.
type runNetwork struct {
	ID     string
	Name   string
	Bridge string
	Subnet string
}

// prepareNetwork creates the run's network and installs its egress rules.
//
// A run with no allowlisted host never reaches here: it gets `NetworkMode: "none"`,
// which is a stronger statement than any rule set, because there is no interface to
// filter.
func (d *Docker) prepareNetwork(ctx context.Context, spec Spec) (runNetwork, error) {
	name := networkName(spec.RunID)

	// The bridge interface name is chosen rather than generated, because the
	// firewall rules match on it and Linux caps an interface name at 15 characters.
	bridge := "qv-" + strings.ReplaceAll(strings.ToLower(spec.RunID), "-", "")
	if len(bridge) > 15 {
		bridge = bridge[:15]
	}

	disabled := false
	created, err := d.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver:     "bridge",
		EnableIPv6: &disabled,

		// Not internal: an internal network blocks the target as well, and the point
		// is a network that reaches exactly one place.
		Internal: false,

		Options: map[string]string{
			"com.docker.network.bridge.name": bridge,

			// No inter-container communication: two runs that somehow shared a
			// network still could not talk to each other.
			"com.docker.network.bridge.enable_icc": "false",

			// Nothing on the host may reach in.
			"com.docker.network.bridge.enable_ip_masquerade": "true",
		},
		Labels: map[string]string{
			"qavia.owner": "qavia",
			"qavia.run":   spec.RunID,
		},
	})
	if err != nil {
		return runNetwork{}, fmt.Errorf("create the run network: %w", err)
	}

	inspected, err := d.client.NetworkInspect(ctx, created.ID, client.NetworkInspectOptions{})
	if err != nil {
		d.removeNetwork(ctx, created.ID)
		return runNetwork{}, fmt.Errorf("inspect the run network: %w", err)
	}

	subnet := ""
	for _, config := range inspected.Network.IPAM.Config {
		if config.Subnet.IsValid() {
			subnet = config.Subnet.String()
			break
		}
	}
	if subnet == "" {
		d.removeNetwork(ctx, created.ID)
		return runNetwork{}, fmt.Errorf("the run network has no subnet, so its egress cannot be filtered")
	}

	prepared := runNetwork{ID: created.ID, Name: name, Bridge: bridge, Subnet: subnet}

	if err := d.installEgressRules(ctx, spec, prepared); err != nil {
		d.removeNetwork(ctx, created.ID)
		return runNetwork{}, err
	}

	return prepared, nil
}

// installEgressRules writes the run's allowlist into the host firewall.
//
// Two attachment points, because they carry different traffic. DOCKER-USER filters
// FORWARD, which is everything leaving the bridge for somewhere else. INPUT carries
// traffic addressed to the host itself, which FORWARD never sees: without the second
// rule a run could reach a service listening on the Docker bridge address, and the
// host is not on any allowlist.
func (d *Docker) installEgressRules(ctx context.Context, spec Spec, prepared runNetwork) error {
	chain := chainName(spec.RunID)

	script := &strings.Builder{}
	fmt.Fprintf(script, "set -eu\n")

	// Idempotent setup: a retried run must not fail because its chain already
	// exists, and must not inherit rules from the attempt before it.
	fmt.Fprintf(script, "iptables -N %s 2>/dev/null || iptables -F %s\n", chain, chain)
	fmt.Fprintf(script, "iptables -C DOCKER-USER -i %s -j %s 2>/dev/null || iptables -I DOCKER-USER 1 -i %s -j %s\n",
		prepared.Bridge, chain, prepared.Bridge, chain)
	fmt.Fprintf(script, "iptables -C INPUT -i %s -j %s 2>/dev/null || iptables -I INPUT 1 -i %s -j %s\n",
		prepared.Bridge, chain, prepared.Bridge, chain)

	// Return traffic for a connection the run was allowed to open. Without this the
	// allowlist would permit the request and drop the response.
	fmt.Fprintf(script, "iptables -A %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n", chain)

	// The metadata endpoints are dropped before the allowlist is consulted, so an
	// allowlist entry that resolves to one of them still cannot reach it. This is
	// the network-layer block F-7.5 asks for; an application-level check is not a
	// control.
	for _, blocked := range metadataRanges {
		fmt.Fprintf(script, "iptables -A %s -d %s -j DROP\n", chain, blocked)
	}

	for _, allowed := range spec.Egress.AllowedHosts {
		address, err := netip.ParseAddr(allowed.IP)
		if err != nil {
			return fmt.Errorf("allowlisted host %q has an unusable address %q: %w",
				allowed.Host, allowed.IP, err)
		}
		if !address.Is4() {
			// IPv6 is disabled on the run network, so an IPv6-only target is a clear
			// failure rather than a run that mysteriously cannot connect.
			return fmt.Errorf("allowlisted host %q resolved to IPv6 (%s), which the runner network does not carry",
				allowed.Host, allowed.IP)
		}
		fmt.Fprintf(script, "iptables -A %s -d %s/32 -p tcp -j ACCEPT\n", chain, address.String())
	}

	// Everything else. This line is the control: the rules above are the exceptions
	// to it, and there is no path around it for traffic on this bridge.
	fmt.Fprintf(script, "iptables -A %s -j REJECT --reject-with icmp-admin-prohibited\n", chain)

	output, err := d.runFirewallHelper(ctx, script.String())
	if err != nil {
		return fmt.Errorf("install egress rules for run %s: %w: %s", spec.RunID, err, output)
	}
	return nil
}

// metadataRanges are the IPv4 link-local ranges every major cloud serves instance
// credentials from. Dropped unconditionally, on every run, allowlist or not.
//
// IPv4 only, because the run network is created with IPv6 disabled and an
// allowlisted host that resolves to IPv6 is refused above. If IPv6 is ever enabled
// on the run network, fd00:ec2::254 has to be dropped in ip6tables here or the
// block has a hole.
var metadataRanges = []string{
	"169.254.0.0/16",     // AWS, GCP, Azure, DigitalOcean: IMDS everywhere.
	"100.100.100.200/32", // Alibaba Cloud.
	"192.0.0.192/32",     // Oracle Cloud.
}

// removeEgressRules detaches and deletes the run's chain.
//
// Order matters: a chain cannot be deleted while a rule jumps to it, so the jumps
// go first. Failures are logged rather than returned, because this runs on the
// cleanup path and a rule that is already gone is the outcome we wanted.
func (d *Docker) removeEgressRules(ctx context.Context, runID, bridge string) {
	chain := chainName(runID)

	script := &strings.Builder{}
	fmt.Fprintf(script, "set -u\n")
	if bridge != "" {
		fmt.Fprintf(script, "iptables -D DOCKER-USER -i %s -j %s 2>/dev/null || true\n", bridge, chain)
		fmt.Fprintf(script, "iptables -D INPUT -i %s -j %s 2>/dev/null || true\n", bridge, chain)
	} else {
		// Sweeping a run whose bridge name is unknown: strip every jump to the
		// chain, whichever interface it was attached to.
		fmt.Fprintf(script, `for table in DOCKER-USER INPUT; do
  while iptables -S "$table" 2>/dev/null | grep -q -- "-j %s"; do
    rule=$(iptables -S "$table" | grep -- "-j %s" | head -1 | sed 's/^-A /-D /')
    # shellcheck disable=SC2086
    iptables $rule || break
  done
done
`, chain, chain)
	}
	fmt.Fprintf(script, "iptables -F %s 2>/dev/null || true\n", chain)
	fmt.Fprintf(script, "iptables -X %s 2>/dev/null || true\n", chain)

	if output, err := d.runFirewallHelper(ctx, script.String()); err != nil {
		slog.WarnContext(ctx, "remove egress rules",
			"run_id", runID, "chain", chain, "error", err, "output", output)
	}
}

// removeNetwork deletes the run's network. Logged, not returned, for the same
// reason as the rules: this is cleanup.
func (d *Docker) removeNetwork(ctx context.Context, networkID string) {
	if _, err := d.client.NetworkRemove(ctx, networkID, client.NetworkRemoveOptions{}); err != nil &&
		!cerrdefs.IsNotFound(err) {
		slog.WarnContext(ctx, "remove run network", "network", networkID, "error", err)
	}
}

// firewallHelperTimeout bounds the helper. Programming a handful of rules takes
// milliseconds; anything longer means the helper is stuck and the run should fail
// closed rather than start with an unknown rule set.
const firewallHelperTimeout = 30 * time.Second

// runFirewallHelper executes a script in the host network namespace.
//
// The helper is the only container this platform ever gives a capability to, and it
// gets exactly two: NET_ADMIN to write rules and NET_RAW because iptables opens a
// raw socket to talk to the kernel. It has no volumes, runs one script, and is
// removed immediately.
func (d *Docker) runFirewallHelper(ctx context.Context, script string) (string, error) {
	helperCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), firewallHelperTimeout)
	defer cancel()

	created, err := d.client.ContainerCreate(helperCtx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        d.helperImage,
			Cmd:          []string{"sh", "-c", script},
			AttachStdout: true,
			AttachStderr: true,
			Labels: map[string]string{
				"qavia.owner": "qavia",
				"qavia.role":  "firewall-helper",
			},
		},
		HostConfig: &container.HostConfig{
			// The host's network namespace, because the rules being written are the
			// host's. This is why the helper exists as its own container: nothing
			// else in the platform gets this.
			NetworkMode: "host",

			CapAdd:         []string{"NET_ADMIN", "NET_RAW"},
			ReadonlyRootfs: true,
			AutoRemove:     false,
			RestartPolicy:  container.RestartPolicy{Name: "no"},
			Resources: container.Resources{
				Memory:     64 << 20,
				MemorySwap: 64 << 20,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create the firewall helper: %w", err)
	}
	defer d.reap(helperCtx, created.ID)

	if _, err := d.client.ContainerStart(helperCtx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start the firewall helper: %w", err)
	}

	wait := d.client.ContainerWait(helperCtx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})

	var status int64
	select {
	case waitErr := <-wait.Error:
		return "", fmt.Errorf("wait for the firewall helper: %w", waitErr)
	case <-helperCtx.Done():
		return "", fmt.Errorf("the firewall helper did not finish: %w", helperCtx.Err())
	case result := <-wait.Result:
		status = result.StatusCode
	}

	logs, logErr := d.client.ContainerLogs(helperCtx, created.ID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	output := ""
	if logErr == nil {
		buffer := &boundedBuffer{limit: 64 << 10}
		if err := demultiplex(logs, buffer); err != nil {
			slog.WarnContext(ctx, "read firewall helper output", "error", err)
		}
		if err := logs.Close(); err != nil {
			slog.WarnContext(ctx, "close firewall helper log stream", "error", err)
		}
		output = strings.TrimSpace(buffer.String())
	}

	if status != 0 {
		return output, fmt.Errorf("the firewall helper exited %d", status)
	}
	return output, nil
}

// networkConfigFor attaches the container to its own network.
func networkConfigFor(prepared runNetwork) *network.NetworkingConfig {
	if prepared.ID == "" {
		return nil
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			prepared.Name: {NetworkID: prepared.ID},
		},
	}
}
