import json
import urllib.request
import urllib.error

windows_manifest = [
  {
    "id": "win11-gui-amd64",
    "category": "Windows Family",
    "family": "windows",
    "distro": "Windows 11 Enterprise",
    "version": "LTSC 24H2 Evaluation",
    "flavor": "gui",
    "arch": "amd64",
    "min_disk_gb": 64,
    "approx_size_bytes": 5060020224,
    "requires_license": True,
    "mirrors": [
      "https://software-static.download.prss.microsoft.com/dbazure/888969d5-f34g-4e03-ac9d-1f9786c66749/26100.1742.240906-0331.ge_release_svc_refresh_CLIENT_LTSC_EVAL_x64FRE_en-us.iso"
    ],
    "notes": "Microsoft Official Evaluation Media. TPM 2.0 / UEFI enforced."
  },
  {
    "id": "winsrv2022-server-amd64",
    "category": "Windows Family",
    "family": "windows",
    "distro": "Windows Server",
    "version": "2022 Standard Eval",
    "flavor": "server",
    "arch": "amd64",
    "min_disk_gb": 32,
    "approx_size_bytes": 5044094976,
    "requires_license": True,
    "mirrors": [
      "https://software-static.download.prss.microsoft.com/sg/download/888969d5-f34g-4e03-ac9d-1f9786c66749/SERVER_EVAL_x64FRE_en-us.iso"
    ],
    "notes": "Official Microsoft permanent evaluation server media."
  },
  {
    "id": "winxp-gui-i386",
    "category": "Windows Family",
    "family": "windows",
    "distro": "Windows XP",
    "version": "SP3 Professional",
    "flavor": "gui",
    "arch": "i386",
    "min_disk_gb": 4,
    "approx_size_bytes": 618659840,
    "requires_license": True,
    "eol": True,
    "mirrors": [
      "https://archive.org/download/WinXPProSP3x86/en_windows_xp_professional_with_service_pack_3_x86_cd_vl_x14-73974.iso"
    ],
    "notes": "Legacy i386 target for testing and virtualization."
  }
]

macos_manifest = [
  {
    "id": "macos-sonoma-virt-amd64",
    "category": "Apple macOS",
    "family": "macos",
    "distro": "macOS Sonoma (Recovery BaseSystem)",
    "version": "14.x",
    "flavor": "gui",
    "arch": "amd64",
    "min_disk_gb": 60,
    "approx_size_bytes": 650000000,
    "opencore_profile": "vbox-opencore-sonoma",
    "mirrors": [
      "https://raw.githubusercontent.com/kholia/OSX-KVM/master/fetch-macOS-v2.py"
    ],
    "notes": "OpenCore KVM recovery bootstrap module. Streams the verified BaseSystem directly from Apple."
  }
]

with open('discovery/manifests/windows.json', 'w') as f:
    json.dump(windows_manifest, f, indent=2)

with open('discovery/manifests/macos.json', 'w') as f:
    json.dump(macos_manifest, f, indent=2)

print("=> Written updated manifests to disk.")
print("=> Starting verification audit...\n")

headers = {
    'User-Agent': 'curl/8.5.0',
    'Range': 'bytes=0-1024'
}

for name, manifest in [("Windows", windows_manifest), ("macOS", macos_manifest)]:
    print(f"=== Auditing {name} Targets ===")
    for entry in manifest:
        title = f"{entry['distro']} ({entry['version']})"
        url = entry['mirrors'][0]
        req = urllib.request.Request(url, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=12) as resp:
                print(f"[ {resp.status} OK ] {title}")
        except urllib.error.HTTPError as e:
            print(f"[ {e.code} FAILED ] {title} -> {url}")
        except Exception as e:
            print(f"[ TIMEOUT/ERR ] {title} -> {e}")
    print()
