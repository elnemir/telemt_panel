set -eu
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
[ "$(id -u)" = 0 ] && [ "$(uname -s)" = Linux ] && [ -d /run/systemd/system ] || exit 11
for command in systemctl ss tar flock install mktemp getent; do
    command -v "$command" >/dev/null || exit 11
done
read -r _ systemd_version _ < <(systemctl --version)
case "$systemd_version" in ''|*[!0-9]*) exit 11 ;; esac
[ "$systemd_version" -ge 247 ] || exit 11
arch=$(uname -m)
case "$arch" in x86_64|aarch64) ;; *) exit 11 ;; esac
for user in telemt telemt-agent; do
    if getent passwd "$user" >/dev/null; then exit 12; fi
done

for path in /opt/telemt-node /var/lib/telemt-node /var/lib/telemt-agent /var/lib/private/telemt-node /var/lib/private/telemt-agent /usr/local/bin/telemt /usr/bin/telemt /etc/telemt /etc/telemt-agent /run/telemt-node-control /run/telemt-node-agent; do
    if [ -e "$path" ] || [ -L "$path" ]; then exit 12; fi
done
for unit in telemt-node-control.socket telemt-node-control@.service; do
    probe=$unit
    if [ "$unit" = telemt-node-control@.service ]; then probe=telemt-node-control@install-probe.service; fi
    [ "$(systemctl show "$probe" --property=LoadState --value)" = not-found ] || exit 12
    for path in "/etc/systemd/system/$unit" "/etc/systemd/system/$unit.d" "/etc/systemd/system/sockets.target.wants/$unit"; do
        [ ! -e "$path" ] && [ ! -L "$path" ] || exit 12
    done
done
for unit in telemt.service telemt-node-gateway.service telemt-agent.service; do
    [ "$(systemctl show "$unit" --property=LoadState --value)" = not-found ] || exit 12
    [ ! -e "/etc/systemd/system/$unit" ] && [ ! -L "/etc/systemd/system/$unit" ] || exit 12
    [ ! -e "/etc/systemd/system/$unit.d" ] && [ ! -L "/etc/systemd/system/$unit.d" ] || exit 12
    [ ! -e "/etc/systemd/system/multi-user.target.wants/$unit" ] && [ ! -L "/etc/systemd/system/multi-user.target.wants/$unit" ] || exit 12
done
for port in "$1" "$2" 9091 9092; do
    listeners=$(ss -H -ltn "sport = :$port") || exit 11
    [ -z "$listeners" ] || exit 13
done
read -r peer _ <<< "${SSH_CONNECTION:?}"
printf '%s\n%s\n' "$arch" "$peer"
