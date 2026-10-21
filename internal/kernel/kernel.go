package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ColaCloudNet/Xboard-Node/internal/model"
	"golang.org/x/time/rate"
)

type Capabilities struct {
	PerUserSpeedLimit    bool
	DeviceLimit          bool
	BuiltInTrafficStats  bool
	AliveIPTracking      bool
	ForceCloseConnection bool
	ForceCloseUser       bool
}

// TLSCert holds PEM-encoded TLS certificate material.
// All cert modes (self-signed, file, ACME, panel-content) normalise to PEM
// so kernels never need to deal with file paths.
type TLSCert struct {
	CertPEM []byte
	KeyPEM  []byte
}

// HasCert reports whether a valid cert+key pair is available.
func (t TLSCert) HasCert() bool {
	return len(t.CertPEM) > 0 && len(t.KeyPEM) > 0
}

// Kernel is the interface for proxy kernel backends (sing-box, xray, etc.).
//
// The interface is split into lifecycle, user management, and observability
// groups. User operations (AddUsers/RemoveUsers) are designed to be atomic
// and non-disruptive — they MUST NOT restart listeners or drop existing
// connections. Only Start and Reload may (re)bind ports.
//
// Implementors: singbox.SingBox, xray.Xray
type Kernel interface {
	// ─── Identity ───────────────────────────────────────────────────────
	// Name returns the kernel identifier (e.g. "sing-box", "xray").
	Name() string
	// Protocols returns the protocol names this kernel supports.
	Protocols() []string
	// Capabilities returns the explicit runtime semantics supported by the kernel.
	Capabilities() Capabilities

	// ─── Lifecycle ──────────────────────────────────────────────────────
	// Start initialises the kernel with the given node config and initial
	// user set, binds listeners, and begins accepting connections.
	// Calling Start on an already-running kernel stops the old instance first.
	Start(nodeConfig *model.NodeSpec, users []model.UserSpec, tls TLSCert) error
	// Stop gracefully shuts down the kernel, draining active connections.
	Stop()
	// IsRunning returns whether the kernel is currently accepting connections.
	IsRunning() bool
	// Reload re-generates the full config and hot-swaps listeners/routes.
	// Use this when port, protocol, or TLS settings change.
	// Existing connections MAY be briefly interrupted.
	Reload(nodeConfig *model.NodeSpec, users []model.UserSpec, tls TLSCert) error

	// ─── User management (non-disruptive) ───────────────────────────────
	// AddUsers registers new users with the running kernel.
	// Existing connections are unaffected. Returns the count actually added
	// (duplicates are silently skipped).
	AddUsers(users []model.UserSpec) (added int, err error)
	// RemoveUsers deregisters users from the running kernel.
	// Active connections of removed users are terminated. Returns the count
	// actually removed (unknown IDs are silently skipped).
	RemoveUsers(users []model.UserSpec) (removed int, err error)
	// UpdateUsers replaces the entire user set atomically.
	// This is equivalent to computing the diff and calling Add/Remove, but
	// may be more efficient for bulk changes. Returns (added, removed) counts.
	UpdateUsers(users []model.UserSpec) (added, removed int, err error)

	// ─── Observability ──────────────────────────────────────────────────
	// GetUserTraffic returns per-user cumulative traffic counters and
	// per-user alive IP sets. The kernel maintains these internally using
	// per-user atomic counters — no per-connection iteration needed.
	// aliveIPs maps userID → set of source IPs currently connected.
	// traffic maps userID → [upload, download] cumulative bytes.
	// connCount is the total number of active connections (for metrics).
	GetUserTraffic(ctx context.Context) (traffic map[int][2]int64, aliveIPs map[int]map[string]bool, connCount int, err error)
	// CloseConnection terminates a specific connection by ID.
	CloseConnection(ctx context.Context, connID string) error
	// CloseUserConnections terminates all connections for the given user UUID.
	CloseUserConnections(ctx context.Context, uuid string) error
	// SetSpeedLimitFunc configures per-user bandwidth throttling.
	// The function resolves a user UUID to a *rate.Limiter (nil = unlimited).
	SetSpeedLimitFunc(fn func(uuid string) *rate.Limiter)
	// SetDeviceLimitFunc configures per-user device limit gate-keeping.
	// The function resolves a user UUID to (limit, hasLimit).
	// Kernels that already gate-keep internally (e.g. xray) may no-op.
	SetDeviceLimitFunc(fn func(uuid string) (int, bool))
	// UpdateGlobalDevices updates the global device state from panel (for multi-node).
	UpdateGlobalDevices(users map[int][]string)
	// ClearGlobalDevices clears the global device state (on WS disconnect).
	ClearGlobalDevices()
}

// ComputeHash returns a hash of config + user identities that would
// require a kernel restart/reconstruction if changed.
func ComputeHash(nc *model.NodeSpec, users []model.UserSpec) string {
	h := sha256.New()
	configData, _ := json.Marshal(nc)
	h.Write(configData)
	sorted := make([]model.UserSpec, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, u := range sorted {
		fmt.Fprintf(h, "%d:%s,", u.ID, u.UUID)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// UserDiff computes which users to add and which to remove when transitioning
// from oldUsers to newUsers. This is a pure helper used by callers; kernels
// may also use it internally.
func UserDiff(oldUsers, newUsers []model.UserSpec) (toAdd, toRemove []model.UserSpec) {
	oldMap := make(map[int]model.UserSpec, len(oldUsers))
	for _, u := range oldUsers {
		oldMap[u.ID] = u
	}
	newMap := make(map[int]model.UserSpec, len(newUsers))
	for _, u := range newUsers {
		newMap[u.ID] = u
	}

	for _, u := range newUsers {
		old, exists := oldMap[u.ID]
		if !exists || old.UUID != u.UUID {
			toAdd = append(toAdd, u)
		}
	}
	for _, u := range oldUsers {
		if _, exists := newMap[u.ID]; !exists {
			toRemove = append(toRemove, u)
		}
	}
	return
}

// RemovedUUIDs returns UUIDs present in oldUsers and absent from newUsers.
// Order follows oldUsers. Empty and duplicate UUIDs are reported once.
// A UUID that moved to a different user ID is kept: it is still authorized.
func RemovedUUIDs(oldUsers, newUsers []model.UserSpec) []string {
	keep := make(map[string]struct{}, len(newUsers))
	for _, u := range newUsers {
		if u.UUID == "" {
			continue
		}
		keep[u.UUID] = struct{}{}
	}
	seen := make(map[string]struct{})
	var removed []string
	for _, u := range oldUsers {
		if u.UUID == "" {
			continue
		}
		if _, ok := keep[u.UUID]; ok {
			continue
		}
		if _, dup := seen[u.UUID]; dup {
			continue
		}
		seen[u.UUID] = struct{}{}
		removed = append(removed, u.UUID)
	}
	return removed
}

// inboundStructureView is the subset of NodeSpec that requires inbound recreate
// when changed. Volatile / hot-reloadable fields (users, routes, KernelLogLevel,
// CertConfig PEM, custom outbounds) are intentionally excluded.
type inboundStructureView struct {
	Protocol            string
	ListenIP            string
	ServerPort          int
	Network             string
	NetworkSettings     map[string]any
	Cipher              string
	Plugin              string
	PluginOpt           string
	ServerKey           string
	TLS                 int
	Flow                string
	Decryption          string
	TLSSettings         map[string]any
	Host                string
	ServerName          string
	Domain              string
	AutoTLS             bool
	Version             int
	UpMbps              int
	DownMbps            int
	Obfs                string
	ObfsPassword        string
	CongestionControl   string
	PaddingScheme       string
	Transport           string
	TrafficPattern      string
	Multiplex           *model.MultiplexConfig
	AcceptProxyProtocol bool
}

// auxRestartView covers non-inbound NodeSpec fields that still need a full
// kernel restart (sing-box cannot hot-swap outbounds after v1.14 Remove removal).
type auxRestartView struct {
	KernelType      string
	CustomOutbounds []model.OutboundConfig
}

func normalizeAnyMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// ComputeInboundStructureHash hashes inbound-relevant NodeSpec fields only.
// User lists, routes, KernelLogLevel, and cert PEM are excluded so panel sync
// noise does not false-trigger inbound recreate.
func ComputeInboundStructureHash(nc *model.NodeSpec) string {
	if nc == nil {
		return ""
	}
	view := inboundStructureView{
		Protocol:            nc.Protocol,
		ListenIP:            nc.ListenIP,
		ServerPort:          nc.ServerPort,
		Network:             nc.Network,
		NetworkSettings:     normalizeAnyMap(nc.NetworkSettings),
		Cipher:              nc.Cipher,
		Plugin:              nc.Plugin,
		PluginOpt:           nc.PluginOpt,
		ServerKey:           nc.ServerKey,
		TLS:                 nc.TLS,
		Flow:                nc.Flow,
		Decryption:          nc.Decryption,
		TLSSettings:         normalizeAnyMap(nc.TLSSettings),
		Host:                nc.Host,
		ServerName:          nc.ServerName,
		Domain:              nc.Domain,
		AutoTLS:             nc.AutoTLS,
		Version:             nc.Version,
		UpMbps:              nc.UpMbps,
		DownMbps:            nc.DownMbps,
		Obfs:                nc.Obfs,
		ObfsPassword:        nc.ObfsPassword,
		CongestionControl:   nc.CongestionControl,
		PaddingScheme:       nc.PaddingScheme,
		Transport:           nc.Transport,
		TrafficPattern:      nc.TrafficPattern,
		Multiplex:           nc.Multiplex,
		AcceptProxyProtocol: nc.AcceptProxyProtocol,
	}
	data, _ := json.Marshal(view)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// ComputeAuxRestartHash hashes NodeSpec fields that are not inbound structure
// but still require a full kernel restart when changed (e.g. custom outbounds).
func ComputeAuxRestartHash(nc *model.NodeSpec) string {
	if nc == nil {
		return ""
	}
	view := auxRestartView{
		KernelType:      nc.KernelType,
		CustomOutbounds: nc.CustomOutbounds,
	}
	if len(view.CustomOutbounds) == 0 {
		view.CustomOutbounds = nil
	}
	data, _ := json.Marshal(view)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// InboundNeedsRecreate reports whether a Reload must fall back to full kernel
// restart because inbound structure, aux config, or TLS material changed.
// User-only and route-only changes return false.
func InboundNeedsRecreate(old, new *model.NodeSpec, tlsChanged bool) bool {
	if tlsChanged {
		return true
	}
	if old == nil || new == nil {
		return old != new
	}
	if ComputeInboundStructureHash(old) != ComputeInboundStructureHash(new) {
		return true
	}
	return ComputeAuxRestartHash(old) != ComputeAuxRestartHash(new)
}

// ComputeUserIdentityHash hashes user ID+UUID only (limits excluded).
func ComputeUserIdentityHash(users []model.UserSpec) string {
	sorted := make([]model.UserSpec, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := sha256.New()
	for _, u := range sorted {
		fmt.Fprintf(h, "%d:%s,", u.ID, u.UUID)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
