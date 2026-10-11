# Mieru (sing-box only)

Xboard-Node runs **mieru** exclusively on the **sing-box** kernel. There is no Xray mieru inbound.

## Panel contract

| Field | Required | Notes |
| --- | --- | --- |
| `protocol` | yes | `mieru` |
| `server_port` | yes | listen port |
| `transport` | no | v1 **TCP only**. Empty → `TCP`. `UDP` and other values are rejected. |
| `traffic_pattern` | no | base64 string (sing-box passthrough) |
| Auth | — | each user: `name` = UUID, `password` = UUID |
| TLS / ACME | — | not used for mieru inbound |
| Multiplex | — | not applied on mieru inbound |

## Kernel selection

- Machine mode and node mode **auto-switch** to `singbox` when the panel node protocol is `mieru`, even if local config says `xray`.
- Do not rely on Xray for mieru; it is unsupported.

## Listen address

sing-box inbounds are built with `"listen": "::"` (dual-stack). Panel `listen_ip` is currently **not** applied to sing-box mieru (or other sing-box inbounds). Bind filtering should be done with host firewall / network namespace if needed.

## Operations

- If the kernel fails to start (bad outbound, port in use, etc.), the process / machine child node **does not auto-retry**. Restart the process (systemd/`xbctl service restart`/container) after fixing the cause.
- Sprint A scope is TCP-only; UDP E2E is out of scope for v1.
