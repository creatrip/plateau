package incus

import _ "embed"

// Shell assets ship inside the binary; provisioning never depends on checkout files.
const containerPackages = `ca-certificates sudo openssh-server tigervnc-standalone-server openbox tint2 hsetroot xterm xfce4-terminal xfce4-settings thunar xdg-utils file fcitx5-hangul fcitx5-frontend-gtk3 dbus-x11 dbus-user-session libpam-systemd x11-xserver-utils novnc chromium fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji`

//go:embed scripts/ssh-setup.sh
var containerSSHSetupScript string

var containerSSHRepairScript = `#!/bin/bash
set -euo pipefail
public_key="$1"
export DEBIAN_FRONTEND=noninteractive
if [ ! -x /usr/sbin/sshd ] || [ ! -x /usr/bin/sudo ]; then
  apt-get update
  apt-get install -y --no-install-recommends openssh-server sudo
fi
if ! id plateau >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash plateau
fi
` + containerSSHSetupScript + `
printf '%s\n' "$public_key" >/etc/ssh/authorized_keys/plateau
chown root:root /etc/ssh/authorized_keys/plateau
chmod 0644 /etc/ssh/authorized_keys/plateau
ssh-keygen -A
sshd -t
systemctl enable ssh.service >/dev/null 2>&1
systemctl reload-or-restart ssh.service
apt-get clean
rm -rf /var/lib/apt/lists/*
`

//go:embed scripts/desktop.sh
var containerDesktopSetupScript string

var containerProvisionScript = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ` + containerPackages + `
if ! id plateau >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash plateau
fi
` + containerSSHSetupScript + `
` + containerDesktopSetupScript + `
systemctl daemon-reload
systemctl enable plateau-vnc.service plateau-novnc.service
apt-get clean
rm -rf /var/lib/apt/lists/*
` + containerImageSanitizeScript

//go:embed scripts/image-sanitize.sh
var containerImageSanitizeScript string

var containerUpdateScript = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get upgrade -y --with-new-pkgs
apt-get install -y --no-install-recommends ` + containerPackages + `
` + containerSSHSetupScript + `
` + containerDesktopSetupScript + `
systemctl daemon-reload
systemctl restart plateau-vnc.service plateau-novnc.service
ssh-keygen -A
sshd -t
systemctl reload-or-restart ssh.service
apt-get clean
rm -rf /var/lib/apt/lists/*
`

var containerManagedImageUpdateScript = containerUpdateScript + containerImageSanitizeScript
