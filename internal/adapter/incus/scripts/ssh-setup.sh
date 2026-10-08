install -d -m 0755 /etc/ssh/authorized_keys /etc/ssh/sshd_config.d
usermod --password '*' plateau
install -d -m 0755 /etc/sudoers.d
printf 'plateau ALL=(ALL:ALL) NOPASSWD: ALL\n' >/etc/sudoers.d/90-plateau
chmod 0440 /etc/sudoers.d/90-plateau
visudo -cf /etc/sudoers.d/90-plateau
cat >/etc/ssh/sshd_config.d/90-plateau.conf <<'EOF'
ListenAddress 127.0.0.1
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile /etc/ssh/authorized_keys/%u
AllowUsers plateau
AllowAgentForwarding no
AllowTcpForwarding no
X11Forwarding no
PermitTunnel no
EOF
cat >/etc/systemd/system/plateau-ssh-hostkeys.service <<'EOF'
[Unit]
Description=Generate Plateau container SSH host keys
Before=ssh.service

[Service]
Type=oneshot
ExecStart=/usr/bin/ssh-keygen -A

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable plateau-ssh-hostkeys.service ssh.service
