# Separate panel and node agent

The central panel can run without a local Telemt process: set top-level
`control_plane_only = true`, remove `[telemt]` URL/credentials and disable the
legacy subscription listener. Configure administrator authentication/TLS and a
persistent `data_dir` as usual. Add nodes through the node selector. No default
node is created; legacy node endpoints return `node_not_found`.

## Agent installation

Build the existing Go command as `telemt-agent` (`go build -o telemt-agent
./cmd/panel`). Mode is selected by `[agent].enabled`, not the executable filename.
It shares typed host adapters with the panel but exposes only its private agent
API; there is no administrative SPA/login. See `agent.example.toml` and
`deploy/telemt-agent.service` (operator-installed examples, not an installer).
Use a separate account and state directory. Set a unique installation ID and a
random Authorization value of at least 32 characters in the service environment
referenced by `agent.authorization_env`. The panel needs the same exact value in
its own environment under the registry's `agent_auth_env` name. Tokens are not
entered into browser forms or returned by the registry. Protect environment
files and backups with owner-only permissions.

The registry accepts `agent_url`, `agent_id`, `agent_auth_env` together. URL and
identity become immutable after binding; the environment variable reference can
change with an `If-Match` registry update. The API URL and agent identity are
separate. Credential rotation requires updating service environments and
restarting the relevant processes, which also closes existing streams.

Use HTTPS with valid trusted certificates. HTTP is accepted only on loopback
(and localhost for the central URL); a loopback reverse proxy/tunnel must provide
the network transport. Both request and response carry `X-Telemt-Agent-ID`.
Authorization and identity checks occur before commands. Redirects are not
followed. Telemt control API connectivity remains a separate requirement: this
agent currently provides service/log operations, not an API tunnel.

The example starts in manual privilege mode. Configure exact service/log rights
using existing host adapters to enable commands; do not run as root merely to
make capability errors disappear. Telemt and agent service bindings must differ.
Agent commands cannot restart/update the panel or accept shell command strings.

The SSH installer on clean Linux hosts enables service controls by default
when the agent is selected. It sets `agent.service_control_socket` and installs
a systemd socket whose handler accepts only start, stop and restart of
`telemt.service` plus a read-only liveness probe. The socket directory is
private and only the agent's mount namespace receives a bind mount of the
socket. No PolicyKit package or sudo rule is needed. If the socket probe fails,
the agent exposes manual service controls and the installer aborts enrollment.

## Command lifecycle

`POST /v1/agent/service/start|stop|restart` requires `Idempotency-Key` with
`<Unix seconds>-<32 lowercase hexadecimal characters>`. The panel generates a
key when absent and returns it in the response header. Keys expire after 24
hours; clocks must be synchronized (up to 60 seconds future skew accepted).

The durable journal saves intent before calling the service adapter. Replays
return the saved HTTP result without repeating the action. A key used for a
different action returns 409. A crash after intent but before confirmed result
returns `agent_command_unconfirmed`; inspect service state before deliberately
issuing a new key. `GET /v1/agent/commands/{key}` exposes the saved result; status
0 means unresolved intent. HTTP 202 confirms command acceptance, not Telemt API
readiness. The journal holds at most 2048 recent records / 2 MiB; capacity or
persistence failure denies new commands. Never delete the journal to retry a
command whose outcome is unknown.

Central `/api/nodes/{id}/manage/telemt/service`, `start`, `stop`, `restart`,
`logs/tail` and `events/logs` forward only to that node's bound agent. No agent
means 501; transport/authentication failure means 502/503, with no local fallback.
Users/config/metrics continue using the independent Telemt API channel. Remote
logs permit only `service=telemt`; logout, session expiry, node retirement and
shutdown cancel streams. Logs retain the central process/session admission caps.

Full file configuration is available when `agent.config_path` and
`agent.config_binary` are set to absolute paths and the installed Telemt binary
advertises `config-file-v1` in `--help`. It runs with the agent service account
and does not elevate file permissions. Use the Configuration files screen for
typed edits, validation, before/after review and recovery drafts. Each apply
changes one existing source, retains include directives and uses Telemt's shared
root lock/revision fence. Up to 32 owner backups (16 MiB encoded each) are kept
in `data_dir/config-backups`, mode 0600; an explicit deletion is required at
capacity. Backups contain secrets and belong in protected backups. Recovery is
a new validated CAS operation, never a blind overwrite. Missing/corrupt sources
or changed include topology require operator repair. The editor does not prove
runtime convergence and never automatically restarts Telemt.

Automatic SSH enrollment can select an operator-supplied `config-file-v1`
Telemt executable using the startup settings `node_install.telemt_binary_file`,
`telemt_binary_sha256` and `telemt_version`. The installer verifies its digest,
and the agent and Telemt share the same unprivileged service identity for the
live configuration directory. Enrollment checks the file endpoint before
registration. The pinned stock build continues to use the Control API forms.
For a managed node with an agent, select **Server → Updates** to update Telemt,
the agent, or both over SSH. This uses the panel’s installation key and verifies
the ED25519 fingerprint supplied by the administrator. Telemt uses the pinned
artifact above and expects its version from `GET /v1/system/info`. The agent
and its HTTPS gateway use the running panel build.
The remote systemd transaction saves the live root TOML and previous binaries,
then checks the pinned Telemt and agent APIs before committing. If file editing
was available, the configuration graph revision must also match. A failed
check, disconnect, or timeout triggers rollback. The panel reports rollback as
confirmed only when both APIs respond with the old state. A hard interruption
can leave `/opt/telemt-node/.panel-upgrade-pending` for operator recovery; check
the services and restore from that directory before clearing it. Completed
snapshots remain in root-only `/opt/telemt-node/upgrade-backup.*`.

For Telemt 3.5.17, `deploy/telemt-3.5.17-config-file.patch` adds the offline
protocol to upstream commit `66708d2866f3f3a4541c4fa67984d408e71a8af4`.
Apply it to that source tree, build `telemt` with `cargo build --release
--locked --bin telemt` for the node's Linux architecture, and verify that
`telemt --help` lists `config-file-v1` before pinning the executable's SHA-256.
