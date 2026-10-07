set -euo pipefail
id recover >/dev/null 2>&1 || useradd --system --home-dir /opt/recover --shell /sbin/nologin recover
id caddy   >/dev/null 2>&1 || useradd --system --home-dir /var/lib/caddy --shell /sbin/nologin caddy
mkdir -p /opt/recover/web /etc/recover /etc/caddy
B=/tmp/recover-bundle
install -m 755 $B/server /opt/recover/server
install -m 755 $B/psp /opt/recover/psp
install -m 755 $B/run-psps.sh /opt/recover/run-psps.sh
cp $B/web/* /opt/recover/web/
chown -R recover:recover /opt/recover
install -m 600 -o root -g root $B/recover.env /etc/recover/recover.env
echo "== caddy: latest release + checksum verification"
curl -fsSL https://api.github.com/repos/caddyserver/caddy/releases/latest -o /tmp/caddy-latest.json
VER=$(grep -m1 '"tag_name"' /tmp/caddy-latest.json | sed 's/.*"v\([^"]*\)".*/\1/')
echo "caddy version: $VER"
cd /tmp
curl -fsSLO https://github.com/caddyserver/caddy/releases/download/v$VER/caddy_${VER}_linux_arm64.tar.gz
curl -fsSLO https://github.com/caddyserver/caddy/releases/download/v$VER/caddy_${VER}_checksums.txt
grep "caddy_${VER}_linux_arm64.tar.gz" caddy_${VER}_checksums.txt | sha512sum -c -
tar -xzf caddy_${VER}_linux_arm64.tar.gz caddy
install -m 755 caddy /usr/local/bin/caddy
/usr/local/bin/caddy version
install -m 644 -o caddy -g caddy $B/Caddyfile /etc/caddy/Caddyfile
/usr/local/bin/caddy validate --config /etc/caddy/Caddyfile 2>&1 | tail -1
install -m 644 $B/recover.service $B/recover-psps.service $B/caddy.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now recover-psps recover caddy
sleep 3
for s in recover-psps recover caddy; do printf "%-14s %s\n" $s "$(systemctl is-active $s)"; done
