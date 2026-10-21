# sing-anytls

Some TLS Proxy Protocol

- 100% compatible with `anytls-go`

## Xboard-Node fork

This tree is `github.com/anytls/sing-anytls` **v0.0.11** (`130d2e61b8895727bfed4942c535e91b246a9603`) plus a local patch. Xboard-Node selects it with:

```
replace github.com/anytls/sing-anytls => ./third_party/sing-anytls
```

`Service.UpdateUsers` used to replace the password map and leave existing `ServerSession`s running. anytls checks the password hash only in the first 32 bytes of a connection; every later stream on that multiplexed session skips authentication. After the panel drops an expired user, a fresh handshake failed but an already-open client kept proxying.

`UpdateUsers` now records each authenticated server session against the password hash that opened it and closes sessions whose password (or the name bound to that password) is no longer in the table. Closing the session closes the underlying connection and every stream on it, so the client cannot open another stream without a new handshake.

The same change is proposed upstream in [anytls/sing-anytls#4](https://github.com/anytls/sing-anytls/pull/4) and is not in v0.0.11 or current `main`. This fork also re-checks the password under the user lock before the session is registered, so a handshake that races with `UpdateUsers` cannot survive removal. No exported API changed.
