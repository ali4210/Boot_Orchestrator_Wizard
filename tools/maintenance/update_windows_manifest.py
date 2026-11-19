import json
import urllib.request
import urllib.error

windows_manifest = [
  {
    "id": "win11-gui-amd64",
    "category": "Windows Family",
    "family": "windows",
    "distro": "Windows 11 Enterprise",
    "version": "23H2/24H2 Evaluation",
    "flavor": "gui",
    "arch": "amd64",
    "min_disk_gb": 64,
    "approx_size_bytes": 5583457280,
    "requires_license": True,
    "mirrors": [
      "https://software-static.download.prss.microsoft.com/dbazure/26100.1.240331-1435.ge_release_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_en-us.iso"
    ],
    "notes": "Official Microsoft permanent Enterprise eval media. TPM 2.0 and UEFI active."
  },
  {
    "id": "win10-gui-amd64",
    "category": "Windows Family",
    "family": "windows",
    "distro": "Windows 10 Enterprise",
    "version": "22H2 Evaluation",
    "flavor": "gui",
    "arch": "amd64",
    "min_disk_gb": 32,
    "approx_size_bytes": 4831838208,
    "requires_license": True,
    "mirrors": [
      "https://software-static.download.prss.microsoft.com/sg/download/888969d5-f34g-4e03-ac9d-1f9786c66749/19045.2006.220908-0225.21h2_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_en-us.iso"
    ],
    "notes": "Official Microsoft permanent Enterprise eval media. Stable desktop release."
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
    "approx_size_bytes": 5033164800,
    "requires_license": True,
    "mirrors": [
      "https://software-static.download.prss.microsoft.com/sg/download/888969d5-f34g-4e03-ac9d-1f9786c66749/SERVER_EVAL_x64FRE_en-us.iso"
    ],
    "notes": "Official Microsoft evaluation media for virtualization."
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

with open('discovery/manifests/windows.json', 'w') as f:
    json.dump(windows_manifest, f, indent=2)

print("=> Successfully updated discovery/manifests/windows.json")
print("=> Auditing Windows mirrors...\n")

headers = {'User-Agent': 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36'}
for entry in windows_manifest:
    title = f"{entry['distro']} ({entry['version']})"
    for idx, url in enumerate(entry['mirrors'], 1):
        req = urllib.request.Request(url, headers=headers, method='HEAD')
        try:
            with urllib.request.urlopen(req, timeout=12) as resp:
                print(f"[ {resp.status} OK ] {title} -> [Mirror {idx}]")
        except urllib.error.HTTPError as e:
            print(f"[ {e.code} FAILED ] {title} -> {url}")
        except Exception as e:
            print(f"[ TIMEOUT/ERR ] {title} -> {e}")
