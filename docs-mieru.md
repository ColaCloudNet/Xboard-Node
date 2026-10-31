# Mieru (sing-box only)

Xboard-Node runs **mieru** exclusively on the **sing-box** kernel. There is no Xray mieru inbound.

## Panel contract

| Field | Required | Notes |
| --- | --- | --- |
| `protocol` | yes | `mieru` |
| `server_port` | yes | listen port |
| `transport` | no | Empty → `BOTH` (listen on TCP and UDP together). Explicit `TCP` or `UDP` stays single-protocol. `BOTH`, `TCP+UDP`, and `TCP/UDP` (either order, spaces ignored) are the dual value. Anything else is rejected. |
| `traffic_pattern` | no | base64 string (sing-box passthrough) |
| Auth | — | each user: `name` = UUID, `password` = UUID |
| TLS / ACME | — | not used for mieru inbound |
| Multiplex | — | not applied on mieru inbound |

## Kernel selection

- Machine mode and node mode **auto-switch** to `singbox` when the panel node protocol is `mieru`, even if local config says `xray`.
- Do not rely on Xray for mieru; it is unsupported.

## Listen address

sing-box inbounds are built with `"listen": "::"` (dual-stack). Panel `listen_ip` is currently **not** applied to sing-box mieru (or other sing-box inbounds). Bind filtering should be done with host firewall / network namespace if needed.

## Transport written into sing-box

The mieru inbound in the pinned sing-box kernel accepts only `"transport": "TCP"` or `"transport": "UDP"`. A value such as `BOTH` or `TCP+UDP` is rejected by sing-box (`transport must be TCP or UDP`).

Dual listen is therefore **two inbounds on the same `listen` and `listen_port`**, the same shape as `third_party/mieru/test/deploy/singbox/singbox-server-config.json`:

| Panel `transport` | sing-box inbounds |
| --- | --- |
| empty, `BOTH`, `TCP+UDP`, `TCP/UDP` | `mieru-tcp` with `"transport": "TCP"`, then `mieru-udp` with `"transport": "UDP"` |
| `TCP` | one inbound, tag `mieru-in`, `"transport": "TCP"` |
| `UDP` | one inbound, tag `mieru-in`, `"transport": "UDP"` |

Strings actually written into each inbound's `transport` field are only `TCP` and `UDP`.

## Operations

- If the kernel fails to start (bad outbound, port in use, etc.), the process / machine child node **does not auto-retry**. Restart the process (systemd/`xbctl service restart`/container) after fixing the cause.
- **Upgrade:** nodes whose panel `transport` is empty used to be normalized to TCP-only. After this change they dual-listen, and the kernel restarts once because the inbound set changes (one `mieru-in` becomes `mieru-tcp` + `mieru-udp`). Nodes that already store `TCP` stay TCP-only; set the field to empty or `BOTH` to turn on UDP as well. No panel schema migration is required.

## TCP underlay idle reclaim

Stock `enfein/mieru` **v3.29.0–v3.38.1** server Mux never reclaims empty TCP underlays after sessions end ([enfein/mieru#317](https://github.com/enfein/mieru/issues/317)): `ESTABLISHED` can keep growing while clients hold or abandon connections. This is **not** kernel `TIME_WAIT` and **not** `CLOSE_WAIT`.

ColaCloudNet ships a patched mieru (via `ColaCloudNet/sing-box` → `third_party/mieru`) that closes server underlays after they have had no live sessions for `ServerUnderlayIdleTimeout` (default ≈ `scheduleIdleTime`, ~2–3 minutes on typical hosts). See [ColaCloudNet/mieru#1](https://github.com/ColaCloudNet/mieru/pull/1).
