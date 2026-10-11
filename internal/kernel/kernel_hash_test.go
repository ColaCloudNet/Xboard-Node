package kernel

import (
	"testing"

	"github.com/ColaCloudNet/Xboard-Node/internal/model"
)

func baseMieruSpec() *model.NodeSpec {
	return &model.NodeSpec{
		Protocol:       "mieru",
		ListenIP:       "0.0.0.0",
		ServerPort:     11451,
		Transport:      "TCP",
		TrafficPattern: "CgYIkgEQzQQ=",
		KernelLogLevel: "info",
	}
}

func TestComputeInboundStructureHash_IgnoresUsersAndLogLevel(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	b.KernelLogLevel = "debug"
	if ComputeInboundStructureHash(a) != ComputeInboundStructureHash(b) {
		t.Fatal("KernelLogLevel must not affect inbound structure hash")
	}

	usersA := []model.UserSpec{{ID: 1, UUID: "u1"}}
	usersB := []model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	// structure hash ignores users by design (users not in NodeSpec)
	_ = usersA
	_ = usersB
	if ComputeInboundStructureHash(a) != ComputeInboundStructureHash(b) {
		t.Fatal("unexpected structure hash drift")
	}
}

func TestComputeInboundStructureHash_HardInboundChange(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	b.ServerPort = 11452
	if ComputeInboundStructureHash(a) == ComputeInboundStructureHash(b) {
		t.Fatal("port change must change inbound structure hash")
	}
	c := baseMieruSpec()
	c.TrafficPattern = "other"
	if ComputeInboundStructureHash(a) == ComputeInboundStructureHash(c) {
		t.Fatal("traffic_pattern change must change inbound structure hash")
	}
	d := baseMieruSpec()
	d.Transport = "UDP"
	if ComputeInboundStructureHash(a) == ComputeInboundStructureHash(d) {
		t.Fatal("transport change must change inbound structure hash")
	}
}

func TestComputeInboundStructureHash_NormalizesEmptyMaps(t *testing.T) {
	a := baseMieruSpec()
	a.NetworkSettings = map[string]any{}
	b := baseMieruSpec()
	b.NetworkSettings = nil
	if ComputeInboundStructureHash(a) != ComputeInboundStructureHash(b) {
		t.Fatal("empty and nil NetworkSettings must hash equal")
	}
}

func TestInboundNeedsRecreate_NoChange(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	b.KernelLogLevel = "warn"
	b.Routes = []model.RouteRule{{ID: 1, Match: []string{"1.1.1.1"}, Action: "block"}}
	if InboundNeedsRecreate(a, b, false) {
		t.Fatal("route/log-only change must not require inbound recreate")
	}
}

func TestInboundNeedsRecreate_UserIrrelevant(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	// users are not on NodeSpec; recreate decision is structure/aux/tls only
	if InboundNeedsRecreate(a, b, false) {
		t.Fatal("identical structure must not recreate")
	}
	if !InboundNeedsRecreate(a, b, true) {
		t.Fatal("tls change must recreate")
	}
}

func TestInboundNeedsRecreate_AuxOutbounds(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	b.CustomOutbounds = []model.OutboundConfig{{
		Tag: "proxy", Protocol: "socks",
		Settings: map[string]any{"server": "1.1.1.1", "server_port": 1080},
	}}
	if !InboundNeedsRecreate(a, b, false) {
		t.Fatal("custom outbound change must require recreate")
	}
}

func TestInboundNeedsRecreate_HardPort(t *testing.T) {
	a := baseMieruSpec()
	b := baseMieruSpec()
	b.ServerPort = 9999
	if !InboundNeedsRecreate(a, b, false) {
		t.Fatal("port change must require recreate")
	}
}

func TestComputeUserIdentityHash_IgnoresLimits(t *testing.T) {
	a := []model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 1, DeviceLimit: 1}}
	b := []model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 99, DeviceLimit: 9}}
	if ComputeUserIdentityHash(a) != ComputeUserIdentityHash(b) {
		t.Fatal("speed/device limits must not affect identity hash")
	}
	c := []model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	if ComputeUserIdentityHash(a) == ComputeUserIdentityHash(c) {
		t.Fatal("user add must change identity hash")
	}
}

func TestComputeHash_StillIncludesUsers(t *testing.T) {
	nc := baseMieruSpec()
	a := ComputeHash(nc, []model.UserSpec{{ID: 1, UUID: "u1"}})
	b := ComputeHash(nc, []model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}})
	if a == b {
		t.Fatal("legacy ComputeHash should still include users")
	}
}
