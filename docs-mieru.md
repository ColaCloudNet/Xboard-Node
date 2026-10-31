# Mieru (sing-box only)

Xboard-Node runs **mieru** exclusively on the **sing-box** kernel. There is no Xray mieru inbound.

## Panel contract

| Field | Required | Notes |
| --- | --- | --- |
| `protocol` | yes | `mieru` |
| `server_port` | yes | listen port |
| `transport` | ignored | Not a listen-mode control. Any value, including `TCP`, `UDP`, or empty, is discarded. An enabled mieru node always listens on TCP and UDP together. |
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

The mieru inbound in the pinned sing-box kernel accepts only `"transport": "TCP"` or `"transport": "UDP"` on a single inbound (`transport must be TCP or UDP`). There is no dual-listen value on one inbound.

Every enabled mieru node is therefore **two inbounds on the same `listen` and `listen_port`**, the same shape as `third_party/mieru/test/deploy/singbox/singbox-server-config.json`:

- `mieru-tcp` with `"transport": "TCP"`
- `mieru-udp` with `"transport": "UDP"`

Those are the only transport strings written into sing-box. The panel `transport` field is not read.

## Operations

- If the kernel fails to start (bad outbound, port in use, etc.), the process / machine child node **does not auto-retry**. Restart the process (systemd/`xbctl service restart`/container) after fixing the cause.
- **Upgrade:** mieru nodes that were TCP-only (the previous empty-transport default, or an explicit panel `TCP` / `UDP`) start listening on both protocols. The inbound set changes from one `mieru-in` to `mieru-tcp` + `mieru-udp`, so the kernel restarts once. No panel edit is required, and changing the panel `transport` value does not select a single protocol.

## TCP underlay idle reclaim

Stock `enfein/mieru` **v3.29.0–v3.38.1** server Mux never reclaims empty TCP underlays after sessions end ([enfein/mieru#317](https://github.com/enfein/mieru/issues/317)): `ESTABLISHED` can keep growing while clients hold or abandon connections. This is **not** kernel `TIME_WAIT` and **not** `CLOSE_WAIT`.

ColaCloudNet ships a patched mieru (via `ColaCloudNet/sing-box` → `third_party/mieru`) that closes server underlays after they have had no live sessions for `ServerUnderlayIdleTimeout` (default ≈ `scheduleIdleTime`, ~2–3 minutes on typical hosts). See [ColaCloudNet/mieru#1](https://github.com/ColaCloudNet/mieru/pull/1).
