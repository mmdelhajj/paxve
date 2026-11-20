#!/usr/bin/env bash
# Install the operations helpers (backups, restore test, Docker clean-up, Cloudflare-only origin, SSH hardening).
# Run as root from /srv/paxve:  bash deploy/ops/install.sh
set -euo pipefail
cd "$(dirname "$0")"
install -m 755 paxve-backup /usr/local/bin/paxve-backup
install -m 755 paxve-restore-test /usr/local/bin/paxve-restore-test
install -m 755 paxve-cf-origin /usr/local/bin/paxve-cf-origin
mkdir -p /etc/paxve && touch /etc/paxve/origin-allow

cat > /etc/cron.d/paxve-backup <<'EOF'
# Paxve: daily encrypted backup, weekly restore test, weekly Docker clean-up (images and build cache only, never volumes)
30 3 * * * root /usr/local/bin/paxve-backup >> /var/log/paxve-backup.log 2>&1
15 4 * * 0 root /usr/local/bin/paxve-restore-test >> /var/log/paxve-restore-test.log 2>&1
45 4 * * 0 root docker builder prune -af --filter until=168h >/dev/null 2>&1; docker image prune -af --filter until=168h >/dev/null 2>&1
EOF

cat > /etc/systemd/system/paxve-cf-origin.service <<'EOF'
[Unit]
Description=Paxve: only Cloudflare may reach ports 80/443
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/paxve-cf-origin
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload

# SSH: keys only, root by key only, no X11
cat > /etc/ssh/sshd_config.d/00-paxve-hardening.conf <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
X11Forwarding no
EOF
sshd -t
echo installed
