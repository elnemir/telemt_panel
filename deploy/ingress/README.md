# Standalone Telemt ingress

This is an operator-managed HAProxy 3.2 deployment. Saving or downloading routing
in the panel does **not** install this service or confirm public connectivity.
The panel, ingress and Telemt nodes may run on different machines.

## Prepare

1. Install HAProxy 3.2 on the ingress host. The example unit expects
   `/usr/sbin/haproxy` and the `haproxy` OS user/group; adjust the unit for the
   host package if they differ. No package is installed by the panel.
2. Point the cluster domain's A/AAAA records to this ingress. Choose different
   public ports: WEB must use 443 with the current Telemt bridge; Native/FakeTLS
   uses another chosen port (the editor initially proposes 8443). Ensure the firewall allows
   these ports and prevents public access to Telemt's backend/control ports.
3. Provision a valid PEM certificate chain plus private key for this domain at
   `/etc/telemt-ingress/tls.pem`, owned by root:haproxy, mode 0640. Keep its parent
   directory root:haproxy mode 0750. Renew using the host's certificate workflow;
   no key is uploaded to the panel. Certificate renewal requires HAProxy reload.
4. Configure each native Telemt listener to accept PROXY protocol and trust
   **only the actual ingress peer CIDRs**. Configure each WEB listener with
   `proxy_protocol = false`, an explicit trusted ingress CIDR and
   `web_client_ip_source = "x_forwarded_for"`. WEB uses a private plain-HTTP hop:
   use a private network or VPN between hosts. Do not expose it on the Internet.
5. Synchronize WEB vhost domain, base path, profiles, secret mode and inner
   `public_addr` across nodes. `public_addr` is the concrete public IP on port
   443 used in the inner KDF tuple; it is not the private WEB listening address.
   Restart-only listener changes must be applied before accepting traffic.
6. In the panel's cluster editor, enter backend traffic addresses (not control
   API URLs), ports, native algorithm, weights 1–256 and drain states. Download
   `haproxy.cfg` after saving. Membership changes require reviewing the inventory
   and exporting again. The certificate path in the export is fixed.

## Validate and install

The following commands run on the ingress host, from the directory containing
the downloaded configuration. They are deployment actions for the operator;
they have not been executed against a live server by this project.

```sh
sudo install -o root -g haproxy -m 0640 haproxy.cfg /etc/telemt-ingress/haproxy.cfg.next
sudo -u haproxy /usr/sbin/haproxy -c -f /etc/telemt-ingress/haproxy.cfg.next
```

Continue only after validation succeeds. For an existing installation, preserve
`haproxy.cfg` as `haproxy.cfg.previous` first; then rename the validated `.next`
file to `haproxy.cfg` on the same filesystem. Install the provided unit as
`/etc/systemd/system/telemt-ingress.service`, run `systemctl daemon-reload`, and
start/enable it for a first installation or reload it for a subsequent change.
The unit validates before start/reload; `USR2` asks the HAProxy master to re-exec
and gracefully replace workers. Existing sessions are not transferred between
Telemt processes. Inspect the service journal and perform external protocol
probes after reload; an accepted reload command alone is not acceptance.

## Verify and recover

- Verify Native/FakeTLS from outside the private network, including client IP
  attribution at Telemt. The native frontend preserves the original byte stream
  and sends the source/destination tuple in PROXY v2.
- Verify WEB TLS, a fresh bridge and its bootstrap/carrier on the chosen public
  port. Keep browser cookies enabled. Test a long-lived connection and reconnect
  after draining/failing a node. The load balancer health check is TCP-only; it
  does not prove WEB capacity, Telegram reachability or paid quota delivery.
- With drain enabled, new native connections avoid that node. Existing TCP and
  cookie-pinned WEB traffic may continue there. To stop existing WEB sessions,
  use Telemt's separate lifecycle controls; drain is not an immediate revoke.
- If validation fails, leave the existing service/file intact. If post-reload
  checks fail, restore the previously validated file and reload. Recheck TLS and
  both protocols. A process restart or failure can require a new WEB session.
- Keep the panel's SQLite DB **and its encryption key** in separate protected
  backups. Do not reclaim unknown quota allocations after restoring an older
  DB. Unknown node grants fail closed; reconciling payments/reservations across
  a stale DB restore requires an explicit recovery procedure.

No multi-ingress failover/state replication, automatic DNS/certificate changes,
remote apply acknowledgement or live acceptance is claimed by this example.
