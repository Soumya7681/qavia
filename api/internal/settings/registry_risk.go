package settings

import "github.com/hyscaler/qavia/api/internal/role"

// Performance and security testing switches (BE-9.5, F-11.5).
//
// Both off by default, and that default is the control rather than a convenience. A
// load test is a denial-of-service attempt and a security scan is an intrusion attempt;
// what makes either one testing rather than an attack is that somebody with the
// authority to authorise it did. A fresh project cannot run either, and turning one on
// is a deliberate act by a lead — audited like every other settings change, because the
// question after a scan is who authorised it (BE-9.5.3).

// CategoryDangerous groups them on the settings screen, named so nobody enables one by
// reflex.
const CategoryDangerous = "Performance and security"

func init() { declareRisk() }

func declareRisk() {
	Declare(Entry{
		Key:      "performance.enabled",
		Category: CategoryDangerous,
		Label:    "Allow performance testing",
		HelpText: "Load testing generates traffic against the target. Off by default: a " +
			"load test pointed at a host you do not own is a denial-of-service attempt. " +
			"The first run against a new host also needs an explicit confirmation naming " +
			"that host.",
		Kind:    KindBool,
		Default: false,
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "security.enabled",
		Category: CategoryDangerous,
		Label:    "Allow security testing",
		HelpText: "Security probes send attack-shaped requests at the target. Off by " +
			"default: pointed at a host you do not own, these are indistinguishable from " +
			"an intrusion attempt. The first run against a new host also needs an explicit " +
			"confirmation naming that host, and the payloads come only from a reviewed " +
			"library — the platform never invents an attack string.",
		Kind:    KindBool,
		Default: false,
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "performance.max_virtual_users",
		Category: CategoryDangerous,
		Label:    "Maximum virtual users",
		HelpText: "A ceiling on the concurrency a load test may request, so a profile " +
			"cannot ask for more load than this installation is willing to generate at a " +
			"target.",
		Kind:    KindInt,
		Default: 100,
		Min:     Bound(1),
		Max:     Bound(10000),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})
}
