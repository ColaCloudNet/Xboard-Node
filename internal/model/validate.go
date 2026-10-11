package model

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ColaCloudNet/Xboard-Node/internal/config"
)

func ValidateNodeSpec(n *NodeSpec, kcfg config.KernelConfig) error {
	if n == nil {
		return nil
	}

	effectiveKernelType := strings.TrimSpace(kcfg.Type)
	if effectiveKernelType == "" {
		effectiveKernelType = strings.TrimSpace(n.KernelType)
	}
	// Protocol-forced kernels (e.g. mieru → singbox) must win so outbound/route
	// validation and runtime checks see the kernel that will actually run.
	effectiveKernelType = ResolveKernelForProtocol(n.Protocol, effectiveKernelType)
	kernelType, err := normalizeKernelType(effectiveKernelType)
	if err != nil {
		return fmt.Errorf("normalize kernel type: %w", err)
	}

	if err := validateMieruFields(n); err != nil {
		return err
	}

	additionalOutboundSources, err := collectAdditionalOutboundTagSources(kcfg.CustomConfig, kcfg.CustomOutbound)
	if err != nil {
		return fmt.Errorf("collect additional outbound tags: %w", err)
	}
	if err := validateOutboundTagCollisions(n.CustomOutbounds, additionalOutboundSources); err != nil {
		return fmt.Errorf("validate outbound tags: %w", err)
	}
	additionalTags := additionalTagNames(additionalOutboundSources)
	availableTags := buildAvailableOutboundTags(n.CustomOutbounds, additionalTags)
	if err := ValidateCustomOutboundsForKernel(n.CustomOutbounds, kernelType, additionalTags); err != nil {
		return fmt.Errorf("validate custom outbounds: %w", err)
	}
	if err := ValidateCustomRouteRules(n.CustomRouteRules, kernelType, availableTags); err != nil {
		return fmt.Errorf("validate custom route rules: %w", err)
	}
	if err := validateTransportKernel(n.Network, kernelType); err != nil {
		return err
	}
	return nil
}

// singboxUnsupportedTransports lists transport types that sing-box does not support.
var singboxUnsupportedTransports = map[string]bool{
	"xhttp":     true,
	"splithttp": true,
}

// singboxOnlyProtocols lists inbound protocols that only exist on sing-box.
var singboxOnlyProtocols = map[string]bool{
	"mieru": true,
}

func validateTransportKernel(network, kernelType string) error {
	net := strings.ToLower(strings.TrimSpace(network))
	if kernelType == "singbox" && singboxUnsupportedTransports[net] {
		return fmt.Errorf("transport %q is not supported by sing-box kernel; use xray kernel instead", net)
	}
	return nil
}

// ResolveKernelForTransport returns the kernel type required by the given
// transport. If the configured kernel cannot handle the transport, it returns
// the kernel that can. Otherwise it returns configuredKernel unchanged.
// This is used in machine mode to auto-switch kernel per node.
func ResolveKernelForTransport(network, configuredKernel string) string {
	net := strings.ToLower(strings.TrimSpace(network))
	cfg := strings.ToLower(strings.TrimSpace(configuredKernel))
	if (cfg == "singbox" || cfg == "sing-box") && singboxUnsupportedTransports[net] {
		return "xray"
	}
	return configuredKernel
}

// ResolveKernelForProtocol returns the kernel required by the inbound protocol.
// Protocols that only exist on sing-box (currently mieru) always resolve to
// "singbox", even when the local config or panel asks for xray.
func ResolveKernelForProtocol(protocol, configuredKernel string) string {
	proto := strings.ToLower(strings.TrimSpace(protocol))
	if singboxOnlyProtocols[proto] {
		return "singbox"
	}
	return configuredKernel
}

// ResolveKernelForNode applies transport then protocol resolution. Protocol
// wins last so mieru cannot be flipped back to xray by an unrelated network=
// field.
func ResolveKernelForNode(protocol, network, configuredKernel string) string {
	resolved := ResolveKernelForTransport(network, configuredKernel)
	return ResolveKernelForProtocol(protocol, resolved)
}

// validateMieruFields normalizes and checks mieru-specific panel fields.
// v1 is TCP-only: empty transport defaults to "TCP"; any other value is rejected.
func validateMieruFields(n *NodeSpec) error {
	if n == nil || !strings.EqualFold(strings.TrimSpace(n.Protocol), "mieru") {
		return nil
	}

	transport := strings.TrimSpace(n.Transport)
	if transport == "" {
		n.Transport = "TCP"
	} else if !strings.EqualFold(transport, "TCP") {
		return fmt.Errorf("mieru transport %q is not supported in v1 (TCP only); got %q", transport, n.Transport)
	} else {
		n.Transport = "TCP"
	}

	if pat := strings.TrimSpace(n.TrafficPattern); pat != "" {
		if _, err := base64.StdEncoding.DecodeString(pat); err != nil {
			return fmt.Errorf("mieru traffic_pattern must be valid base64: %w", err)
		}
	}
	return nil
}

func normalizeKernelType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "singbox", "sing-box":
		return "singbox", nil
	case "xray":
		return "xray", nil
	default:
		return "", fmt.Errorf("unsupported kernel type %q", value)
	}
}

func buildAvailableOutboundTags(structured []OutboundConfig, rawTags []string) map[string]struct{} {
	available := map[string]struct{}{
		"direct": {},
		"block":  {},
	}
	for _, outbound := range structured {
		tag := strings.ToLower(strings.TrimSpace(outbound.Tag))
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	for _, tag := range rawTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	return available
}
