package lima

// Host configuration contains no per-container workload packages.
const incusConfigurationCheck = `incus storage show default >/dev/null
incus network show incusbr0 >/dev/null
test "$(incus profile device get default root pool)" = default
test "$(incus profile device get default eth0 network)" = incusbr0`

const incusServiceCheck = `systemctl is-enabled --quiet incus.service incus.socket
systemctl is-active --quiet incus.service incus.socket`

const hostHealthCheck = `test -x /usr/bin/incus -a -x /usr/bin/newuidmap -a -x /usr/bin/newgidmap -a -x /usr/sbin/dnsmasq -a -x /usr/sbin/mkfs.btrfs -a -x /usr/bin/nc -a -x /usr/bin/zstd
` + incusServiceCheck + `
` + incusConfigurationCheck + `
pool="$(incus storage show plateau)"
printf '%s\n' "$pool" | grep -qx 'driver: btrfs'
printf '%s\n' "$pool" | grep -qx 'status: Created'
test "$(findmnt -n -o FSTYPE --mountpoint /var/lib/incus/storage-pools/plateau)" = btrfs
findmnt -n -o OPTIONS --mountpoint /var/lib/incus/storage-pools/plateau | grep -q 'compress=zstd'
test "$(btrfs property get /var/lib/incus/storage-pools/plateau compression)" = compression=zstd
printf 'ready\n'`

const incusPreseed = `config: {}
networks:
- name: incusbr0
  type: bridge
  config:
    ipv4.address: 10.231.0.1/24
    ipv4.nat: "true"
    ipv6.address: none
storage_pools:
- name: default
  driver: dir
  config: {}
profiles:
- name: default
  devices:
    root:
      type: disk
      path: /
      pool: default
    eth0:
      type: nic
      name: eth0
      network: incusbr0
`

const incusHostTemplate = `minimumLimaVersion: 2.2.0
base:
- template:debian

portForwards:
- guestPortRange: [30000, 39999]
  hostPortRange: [30000, 39999]
  guestIP: "127.0.0.1"
  hostIP: "127.0.0.1"

provision:
- mode: system
  script: |
    #!/bin/sh
    set -eu
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends incus-base uidmap dnsmasq-base btrfs-progs netcat-openbsd zstd
    systemctl enable --now incus.service incus.socket
`
