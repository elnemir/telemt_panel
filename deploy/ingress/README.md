# Telemt ingress

Open **Clusters → Connection balancing**. The panel suggests backend IP
addresses and listener ports for active nodes. Review the addresses from the
load balancer's network. Set the public Native TCP port and balancing strategy.
Enable WEB only if the cluster serves WEB on a common domain. Save the route
and open **Install HAProxy**.

## Prepare the host and the nodes

- Use a Debian or Ubuntu host running systemd with HAProxy 3.0 or newer.
  Authorize the panel installation public key for the UID 0 SSH account.
  Read the ED25519 fingerprint from the trusted load balancer console with
  `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
- Allow incoming TCP on the public Native port. Point the cluster DNS name
  to the load balancer if using a domain. Check that the selected ports are
  available for the new service.
- Native listeners must accept PROXY protocol and trust the load balancer's
  actual peer IP or CIDR. Limit backend access to the load balancer. If the
  same Telemt node also serves direct clients, use a separate listener for
  the load balancer. Check the listener and trust changes on each node before
  moving traffic. Never reuse the Control API port as a backend.
- For WEB, allow TCP 443 and use private networking for backend HTTP. Set
  `proxy_protocol = false`, `web_client_ip_source = "x_forwarded_for"` and
  `web_trusted_proxy_cidrs` to the actual ingress CIDR on each WEB listener.
  Configure the same WEB vhost, base path and profile policies on each node.
  Its `public_addr` is the concrete public IP on port 443 used by the WEB
  bridge. Provision the domain's certificate chain and private key as a single
  PEM at `/etc/telemt-ingress/tls.pem`, owned by root:haproxy with mode 0640.
  The parent directory should be root:haproxy with mode 0750. Reload after
  renewing this certificate.

## Install and apply

Enter the load balancer IP address, SSH port, account, and ED25519
fingerprint, then select **Apply over SSH**. The panel connects with the
configured SSH installation key. It installs missing HAProxy and socat
packages through APT, validates the saved revision as the HAProxy user,
and writes the config atomically to `/etc/telemt-ingress/haproxy.cfg`.
It starts or reloads `telemt-ingress.service` and verifies the active
worker's connection limit through a local socket. The regular
`haproxy.service` has separate configuration and lifecycle.

Reapply from the same step after changing routing. Activation failure
restores the prior file and running service when present. The previous
validated file is retained at `/etc/telemt-ingress/haproxy.cfg.previous`.
A config marked for another cluster, a newer revision or an incompatible
service installation requires operator intervention. The panel shows
recent operation status for one hour. The saved route and last
installation status are separate.

## Capacity profile

The installer chooses HAProxy `maxconn` per worker from the smaller of
one quarter of total memory and one half of available memory. On cgroup v2
it also considers limits and remaining memory in the installation
process's cgroup hierarchy. The budget uses 96 KiB per Native connection
or 192 KiB with WEB enabled. The result is capped at 100,000. Installation
fails below 256. The systemd service has a 262,144 file descriptor limit
and leaves HAProxy thread selection automatic.

The Native listener requests a TCP accept backlog of at least 4,096,
or the computed `maxconn` if larger. Linux caps the effective queue at
`net.core.somaxconn`. This buffers bursts without raising the active
session limit. Check the Send-Q column of `ss -ltn 'sport = :10443'`
(use your route's port) after applying. A graceful reload can reuse an
existing listening socket and leave its previous queue size in effect.
Changing the size on such a host requires recreating the listener in
a maintenance window; the installer does not restart a running service.

Native TCP enables keepalives on both legs, allows one hour of tunnel
inactivity, and releases half-closed sessions after 30 seconds.
Backend checks are staggered. Old workers drain existing connections
on reload. Memory, CPU, network bandwidth, and the outgoing ephemeral
port range per backend endpoint determine actual capacity. Run a load
test with the production mix before setting a concurrency target.

## Verify traffic

Read backend health via the dedicated local HAProxy socket:

```sh
printf '@1 show stat\n' | sudo socat - UNIX-CONNECT:/run/telemt-ingress/master.sock
```

A TCP check shows listener reachability. Verify actual Native/FakeTLS
traffic and the client source IP on Telemt. For WEB, open a fresh bridge
and verify bootstrap, carrier traffic and reconnect after drain.
WEB affinity stays with a node while it is available. A failed node can
require reconnecting with a fresh session.
