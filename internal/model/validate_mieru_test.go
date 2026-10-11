package model

import (
	"strings"
	"testing"

	"github.com/ColaCloudNet/Xboard-Node/internal/config"
	"github.com/ColaCloudNet/Xboard-Node/internal/panel"
)

func TestResolveKernelForProtocol_MieruForcesSingbox(t *testing.T) {
	cases := []struct {
		protocol string
		cfg      string
		want     string
	}{
		{"mieru", "xray", "singbox"},
		{"mieru", "singbox", "singbox"},
		{"MIERU", "xray", "singbox"},
		{"vmess", "xray", "xray"},
		{"shadowsocks", "singbox", "singbox"},
		{"", "xray", "xray"},
	}
	for _, tc := range cases {
		got := ResolveKernelForProtocol(tc.protocol, tc.cfg)
		if got != tc.want {
			t.Errorf("ResolveKernelForProtocol(%q, %q)=%q, want %q", tc.protocol, tc.cfg, got, tc.want)
		}
	}
}

func TestResolveKernelForNode_MieruWinsOverXHTTP(t *testing.T) {
	// Unrelated network=xhttp must not force xray when protocol is mieru.
	got := ResolveKernelForNode("mieru", "xhttp", "xray")
	if got != "singbox" {
		t.Fatalf("got %q, want singbox", got)
	}
	got = ResolveKernelForNode("vmess", "xhttp", "singbox")
	if got != "xray" {
		t.Fatalf("got %q, want xray", got)
	}
}

func TestValidateNodeSpec_MieruEmptyTransportDefaultsTCP(t *testing.T) {
	spec, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:   "mieru",
		ServerPort: 11451,
	}, config.KernelConfig{Type: "xray"}) // xray config must still validate via auto-resolve
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Transport != "TCP" {
		t.Fatalf("Transport=%q, want TCP", spec.Transport)
	}
}

func TestValidateNodeSpec_MieruTCPOK(t *testing.T) {
	spec, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:       "mieru",
		ServerPort:     11451,
		Transport:      "tcp",
		TrafficPattern: "GgQIARAK",
	}, config.KernelConfig{Type: "singbox"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Transport != "TCP" {
		t.Fatalf("Transport=%q, want normalized TCP", spec.Transport)
	}
}

func TestValidateNodeSpec_MieruRejectsUDP(t *testing.T) {
	_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:   "mieru",
		ServerPort: 11451,
		Transport:  "UDP",
	}, config.KernelConfig{Type: "singbox"})
	if err == nil {
		t.Fatal("expected error for UDP transport")
	}
	if !strings.Contains(err.Error(), "TCP only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateNodeSpec_MieruRejectsBadTrafficPattern(t *testing.T) {
	_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:       "mieru",
		ServerPort:     11451,
		Transport:      "TCP",
		TrafficPattern: "%%%not-base64%%%",
	}, config.KernelConfig{Type: "singbox"})
	if err == nil {
		t.Fatal("expected error for bad traffic_pattern")
	}
	if !strings.Contains(err.Error(), "traffic_pattern") {
		t.Fatalf("unexpected error: %v", err)
	}
}
