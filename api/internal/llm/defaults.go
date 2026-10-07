package llm

// Shipped capability defaults, seeded when a model is added (F-16.4).
//
// These are a starting point, not the truth. The Test connection probe runs four
// small calls and writes back what actually worked, and detected capability
// overwrites declared capability, because a table in a source file goes stale the
// moment a provider ships anything.
//
// Defaults are per kind rather than per model on purpose: a per-model table would
// need editing every time a vendor renames a model, and the probe corrects it
// anyway.
var kindDefaults = map[Kind]Capabilities{
	KindAnthropic: {
		ToolUse:          true,
		Vision:           true,
		StructuredOutput: StructuredNative,
		// Explicit cache_control breakpoints. Reads cost roughly a tenth of input,
		// which is what makes a 40-call fan-out over one specification affordable.
		PromptCaching:     CachingExplicit,
		EffortControl:     true,
		MaxToolIterations: 30,
	},
	KindBedrock: {
		ToolUse:           true,
		Vision:            true,
		StructuredOutput:  StructuredToolBased,
		PromptCaching:     CachingExplicit,
		EffortControl:     false,
		MaxToolIterations: 30,
	},
	KindVertex: {
		ToolUse:           true,
		Vision:            true,
		StructuredOutput:  StructuredNative,
		PromptCaching:     CachingExplicit,
		EffortControl:     false,
		MaxToolIterations: 30,
	},
	KindOpenAI: {
		ToolUse:          true,
		Vision:           true,
		StructuredOutput: StructuredNative,
		// Automatic prefix caching with nothing to set. The only requirement is
		// that the prefix stays byte-identical, which is a code invariant here.
		PromptCaching:     CachingAutomatic,
		EffortControl:     true,
		MaxToolIterations: 30,
	},
	KindAzureOpenAI: {
		ToolUse:           true,
		Vision:            true,
		StructuredOutput:  StructuredNative,
		PromptCaching:     CachingAutomatic,
		EffortControl:     true,
		MaxToolIterations: 30,
	},
	KindGemini: {
		ToolUse:          true,
		Vision:           true,
		StructuredOutput: StructuredNative,
		// A separate cache-create API with its own TTL and minimum size. The handle
		// belongs to the job chain, not to a call.
		PromptCaching:     CachingExplicit,
		EffortControl:     false,
		MaxToolIterations: 30,
	},
	KindOpenAICompatible: {
		// Deliberately pessimistic. This kind reaches everything from a 32B model
		// on a workstation to a hosted gateway, so the defaults claim the least and
		// the probe raises them.
		ToolUse:           false,
		Vision:            false,
		StructuredOutput:  StructuredPromptOnly,
		PromptCaching:     CachingNone,
		EffortControl:     false,
		MaxToolIterations: 10,
	},
}

// DefaultCapabilities returns the seed for a kind.
func DefaultCapabilities(kind Kind) Capabilities {
	if defaults, known := kindDefaults[kind]; known {
		return defaults
	}
	return kindDefaults[KindOpenAICompatible]
}

// DefaultResidency is where a kind processes data unless an admin says otherwise.
//
// openai-compatible defaults to local because that is what it is usually pointed
// at, and the failure mode of guessing wrong in that direction is a project
// refusing to run rather than client code leaving the building. Everything else
// defaults to external, which is the conservative answer for a hosted API.
func DefaultResidency(kind Kind) Residency {
	switch kind {
	case KindOpenAICompatible:
		return ResidencyLocal
	case KindBedrock, KindVertex:
		// Regional under an enterprise agreement is the usual arrangement, but that
		// is a contract fact rather than a technical one, so it stays external
		// until an admin records the agreement.
		return ResidencyExternal
	default:
		return ResidencyExternal
	}
}

// UnusableReason says why a model cannot serve a tier, or "" when it can.
//
// The reason is returned to the client so the UI can disable the option and
// explain, rather than hiding it and leaving a user wondering why their model is
// missing (F-16.5).
func UnusableReason(model Model, tier Tier) string {
	switch {
	case !model.Enabled:
		return "This model is disabled."
	case !model.ServesTier(tier):
		return "This model is not offered for the " + string(tier) + " tier."
	case tier == TierVision && !model.Capabilities.Vision:
		return "This model cannot read images, so it cannot serve the vision tier."
	default:
		return ""
	}
}
