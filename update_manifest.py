import json
import urllib.request
import urllib.error

manifest = {
  "entries": [
    # --- Security & Pentesting ---
    {
      "distro": "Kali Linux",
      "version": "2026.2 Installer",
      "codename": "kali-rolling",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "Security & Pentesting",
      "approx_size_bytes": 4294967296,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://cdimage.kali.org/kali-2026.2/kali-linux-2026.2-installer-amd64.iso",
      "mirrors": [
        "https://cdimage.kali.org/kali-2026.2/kali-linux-2026.2-installer-amd64.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Kali Linux",
      "version": "2026.2 NetInst",
      "codename": "kali-rolling",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Security & Pentesting",
      "approx_size_bytes": 629145600,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://cdimage.kali.org/kali-2026.2/kali-linux-2026.2-installer-netinst-amd64.iso",
      "mirrors": [
        "https://cdimage.kali.org/kali-2026.2/kali-linux-2026.2-installer-netinst-amd64.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Parrot OS Minimal Core",
      "version": "latest",
      "codename": "lorikeet",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Security & Pentesting",
      "approx_size_bytes": 450000000,
      "min_disk_gb": 10,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://deb.parrot.sh/direct/parrot/iso/latest/parrot-amd64.tar.xz",
      "mirrors": [
        "https://deb.parrot.sh/direct/parrot/iso/latest/parrot-amd64.tar.xz"
      ],
      "sha256": ""
    },

    # --- General Purpose & Workstation (GUI + TTY) ---
    {
      "distro": "Ubuntu Desktop",
      "version": "24.04.4 LTS",
      "codename": "noble",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 6120349696,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://releases.ubuntu.com/24.04/ubuntu-24.04.4-desktop-amd64.iso",
      "mirrors": [
        "https://releases.ubuntu.com/24.04/ubuntu-24.04.4-desktop-amd64.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Ubuntu Server",
      "version": "24.04.4 LTS",
      "codename": "noble",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Server & Cloud",
      "approx_size_bytes": 2831155200,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://releases.ubuntu.com/24.04/ubuntu-24.04.4-live-server-amd64.iso",
      "mirrors": [
        "https://releases.ubuntu.com/24.04/ubuntu-24.04.4-live-server-amd64.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Linux Mint Cinnamon",
      "version": "22",
      "codename": "wilma",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 2936012800,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://mirrors.kernel.org/linuxmint/stable/22/linuxmint-22-cinnamon-64bit.iso",
      "mirrors": [
        "https://mirrors.kernel.org/linuxmint/stable/22/linuxmint-22-cinnamon-64bit.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Fedora Workstation Live",
      "version": "40 (GNOME)",
      "codename": "fedora-40",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 2254857856,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases/40/Workstation/x86_64/iso/Fedora-Workstation-Live-x86_64-40-1.14.iso",
      "mirrors": [
        "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases/40/Workstation/x86_64/iso/Fedora-Workstation-Live-x86_64-40-1.14.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Fedora Server Netinst",
      "version": "40",
      "codename": "fedora-40",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 812312576,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases/40/Server/x86_64/iso/Fedora-Server-netinst-x86_64-40-1.14.iso",
      "mirrors": [
        "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases/40/Server/x86_64/iso/Fedora-Server-netinst-x86_64-40-1.14.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "openSUSE Tumbleweed Live",
      "version": "GNOME Desktop",
      "codename": "tumbleweed",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 1153433600,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://download.opensuse.org/tumbleweed/iso/openSUSE-Tumbleweed-GNOME-Live-x86_64-Current.iso",
      "mirrors": [
        "https://download.opensuse.org/tumbleweed/iso/openSUSE-Tumbleweed-GNOME-Live-x86_64-Current.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "openSUSE Tumbleweed Netinst",
      "version": "Rolling Netinst",
      "codename": "tumbleweed",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 416284672,
      "min_disk_gb": 20,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://download.opensuse.org/tumbleweed/iso/openSUSE-Tumbleweed-NET-x86_64-Current.iso",
      "mirrors": [
        "https://download.opensuse.org/tumbleweed/iso/openSUSE-Tumbleweed-NET-x86_64-Current.iso"
      ],
      "sha256": ""
    },

    # --- Server & Cloud ---
    {
      "distro": "Debian GNU/Linux",
      "version": "12 Netboot",
      "codename": "bookworm",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Server & Cloud",
      "approx_size_bytes": 60817408,
      "min_disk_gb": 10,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://deb.debian.org/debian/dists/bookworm/main/installer-amd64/current/images/netboot/mini.iso",
      "mirrors": [
        "https://deb.debian.org/debian/dists/bookworm/main/installer-amd64/current/images/netboot/mini.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Arch Linux",
      "version": "Rolling Release",
      "codename": "arch",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "General Purpose & Workstation",
      "approx_size_bytes": 1153433600,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://geo.mirror.pkgbuild.com/iso/latest/archlinux-x86_64.iso",
      "mirrors": [
        "https://geo.mirror.pkgbuild.com/iso/latest/archlinux-x86_64.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Alpine Linux",
      "version": "3.20 Standard",
      "codename": "alpine",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Server & Cloud",
      "approx_size_bytes": 220200960,
      "min_disk_gb": 5,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/alpine-standard-3.20.3-x86_64.iso",
      "mirrors": [
        "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/alpine-standard-3.20.3-x86_64.iso"
      ],
      "sha256": ""
    },

    # --- Enterprise & RHEL Ecosystem (GUI + TTY) ---
    {
      "distro": "CentOS Linux (Vault Legacy)",
      "version": "7.9 Full DVD",
      "codename": "core",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 4700000000,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": True,
      "primary_url": "https://archive.kernel.org/centos-vault/7.9.2009/isos/x86_64/CentOS-7-x86_64-DVD-2009.iso",
      "mirrors": [
        "https://archive.kernel.org/centos-vault/7.9.2009/isos/x86_64/CentOS-7-x86_64-DVD-2009.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "CentOS Linux (Vault Legacy)",
      "version": "7.9 Minimal",
      "codename": "core",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 1020264448,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": True,
      "primary_url": "https://archive.kernel.org/centos-vault/7.9.2009/isos/x86_64/CentOS-7-x86_64-Minimal-2009.iso",
      "mirrors": [
        "https://archive.kernel.org/centos-vault/7.9.2009/isos/x86_64/CentOS-7-x86_64-Minimal-2009.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "CentOS Stream",
      "version": "9 Latest",
      "codename": "stream9",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 950000000,
      "min_disk_gb": 20,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/iso/CentOS-Stream-9-latest-x86_64-boot.iso",
      "mirrors": [
        "https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/iso/CentOS-Stream-9-latest-x86_64-boot.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Rocky Linux Workstation",
      "version": "9.4 (GNOME)",
      "codename": "blue-onyx",
      "flavor": "gui",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 2411724800,
      "min_disk_gb": 25,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://download.rockylinux.org/pub/rocky/9/live/x86_64/Rocky-9-Workstation-x86_64-latest.iso",
      "mirrors": [
        "https://download.rockylinux.org/pub/rocky/9/live/x86_64/Rocky-9-Workstation-x86_64-latest.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "Rocky Linux Minimal",
      "version": "9.4 Minimal",
      "codename": "blue-onyx",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 1782579200,
      "min_disk_gb": 20,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://download.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9-latest-x86_64-minimal.iso",
      "mirrors": [
        "https://download.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9-latest-x86_64-minimal.iso"
      ],
      "sha256": ""
    },
    {
      "distro": "AlmaLinux Minimal",
      "version": "9.4 Minimal",
      "codename": "seafoam-ominyx",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Enterprise & RHEL Ecosystem",
      "approx_size_bytes": 1887436800,
      "min_disk_gb": 20,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://repo.almalinux.org/almalinux/9/isos/x86_64/AlmaLinux-9-latest-x86_64-minimal.iso",
      "mirrors": [
        "https://repo.almalinux.org/almalinux/9/isos/x86_64/AlmaLinux-9-latest-x86_64-minimal.iso"
      ],
      "sha256": ""
    },

    # --- Power User & Source-Based ---
    {
      "distro": "Gentoo Linux (Binary Bridge)",
      "version": "Stage 3 OpenRC",
      "codename": "gentoo",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Power User & Source-Based",
      "approx_size_bytes": 356515840,
      "min_disk_gb": 20,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://distfiles.gentoo.org/releases/amd64/autobuilds/latest-stage3-amd64-openrc.txt",
      "mirrors": [
        "https://distfiles.gentoo.org/releases/amd64/autobuilds/latest-stage3-amd64-openrc.txt"
      ],
      "sha256": ""
    },
    {
      "distro": "Slackware Linux (Mini Install)",
      "version": "15.0",
      "codename": "slackware",
      "flavor": "tty",
      "arch": "amd64",
      "family": "linux",
      "category": "Power User & Source-Based",
      "approx_size_bytes": 146800640,
      "min_disk_gb": 15,
      "requires_license": False,
      "eol": False,
      "primary_url": "https://mirrors.kernel.org/slackware/slackware64-15.0/usb-and-pxe-installers/usbboot.img",
      "mirrors": [
        "https://mirrors.kernel.org/slackware/slackware64-15.0/usb-and-pxe-installers/usbboot.img"
      ],
      "sha256": ""
    }
  ]
}

with open('discovery/manifests/linux.json', 'w') as f:
    json.dump(manifest, f, indent=2)

print(f"=> Successfully wrote {len(manifest['entries'])} entries to discovery/manifests/linux.json")
print("=> Starting network connectivity audit...\n")

headers = {'User-Agent': 'curl/8.5.0'}
for entry in manifest['entries']:
    distro = entry.get('distro', '')
    ver = entry.get('version', '')
    flavor = entry.get('flavor', '').upper()
    url = entry.get('primary_url', '')
    req = urllib.request.Request(url, headers=headers, method='HEAD')
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            print(f"[ {resp.status} OK ] [{flavor}] {distro} ({ver})")
    except urllib.error.HTTPError as e:
        print(f"[ {e.code} FAILED ] [{flavor}] {distro} ({ver}) -> {url}")
    except Exception as e:
        print(f"[ TIMEOUT/ERR ] [{flavor}] {distro} ({ver}) -> {e}")
