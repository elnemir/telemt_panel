set -eu
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 077
stage=$1
root=/opt/telemt-node
system=/etc/systemd/system
exec 8>/run/telemt-node-install.lock
flock -n 8 || exit 14
# Recheck before any writes while holding the installer lock.
bash "$stage/preflight" "$2" "$3" >/dev/null

with_agent=0
if [ -f "$stage/telemt-agent.service" ]; then with_agent=1; fi
made_root=0
made_telemt=0
made_gateway=0
made_agent=0
committed=0
cleanup() {
    code=$?
    trap - EXIT
    if [ "$committed" -eq 0 ]; then
        set +e
        if [ "$made_agent" -eq 1 ]; then
            systemctl disable --now telemt-agent.service >&2 &&
                rm -f "$system/telemt-agent.service" &&
                rm -rf /var/lib/telemt-agent /var/lib/private/telemt-agent || code=25
        fi
        if [ "$made_gateway" -eq 1 ]; then
            systemctl disable --now telemt-node-gateway.service >&2 &&
                rm -f "$system/telemt-node-gateway.service" || code=25
        fi
        if [ "$made_telemt" -eq 1 ]; then
            systemctl disable --now telemt.service >&2 &&
                rm -f "$system/telemt.service" &&
                rm -rf /var/lib/telemt-node /var/lib/private/telemt-node || code=25
        fi
        if [ "$made_root" -eq 1 ] && [ "$code" -ne 25 ]; then rm -rf "$root" || code=25; fi
        systemctl daemon-reload >&2 || code=25
    fi
    rm -rf "$stage" || code=25
    exit "$code"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mkdir -m 0755 "$root"
made_root=1
for name in telemt node-runtime; do install -m 0755 "$stage/$name" "$root/$name"; done
for name in telemt.toml gateway.crt gateway.key; do install -m 0600 "$stage/$name" "$root/$name"; done
if [ "$with_agent" -eq 1 ]; then
    install -m 0600 "$stage/agent.toml" "$stage/agent.env" "$root/"
fi
# Publish only into names which were checked to be absent.
publish() {
    temporary=$(mktemp "$system/.telemt-install.XXXXXXXXXX")
    if ! install -m 0644 "$stage/$1" "$temporary" || ! ln "$temporary" "$system/$1"; then
        rm -f "$temporary"
        return 1
    fi
    rm -f "$temporary"
}
publish telemt.service
made_telemt=1
publish telemt-node-gateway.service
made_gateway=1
if [ "$with_agent" -eq 1 ]; then
    publish telemt-agent.service
    made_agent=1
fi
systemctl daemon-reload >&2
systemctl enable --now telemt.service telemt-node-gateway.service >&2
if [ "$with_agent" -eq 1 ]; then systemctl enable --now telemt-agent.service >&2; fi
printf 'ready\n'
# EOF, timeout and shutdown roll back the files and services of this attempt.
read -r -t 90 decision
case "$decision" in
    commit) committed=1 ;;
    abort) exit 0 ;;
    *) exit 1 ;;
esac
