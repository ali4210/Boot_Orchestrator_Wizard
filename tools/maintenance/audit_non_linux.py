import json
import urllib.request
import urllib.error

def audit_file(filepath, name_key="distro"):
    print(f"==================================================")
    print(f"=> Auditing: {filepath}")
    print(f"==================================================")
    try:
        with open(filepath, 'r') as f:
            raw = f.read()
            # Clean comments if any exist in jsonc
            cleaned = "\n".join([line for line in raw.splitlines() if not line.strip().startswith("//")])
            entries = json.loads(cleaned)
    except Exception as e:
        print(f"Error loading {filepath}: {e}")
        return

    headers = {'User-Agent': 'curl/8.5.0'}
    for entry in entries:
        title = f"{entry.get(name_key, entry.get('id', 'Unknown'))} ({entry.get('version', '')})"
        mirrors = entry.get('mirrors', [])
        if not mirrors and 'primary_url' in entry:
            mirrors = [entry['primary_url']]

        print(f"\nTarget: {title}")
        for idx, url in enumerate(mirrors, 1):
            req = urllib.request.Request(url, headers=headers, method='HEAD')
            try:
                with urllib.request.urlopen(req, timeout=12) as resp:
                    print(f"  [Mirror {idx}] {resp.status} OK -> {url}")
            except urllib.error.HTTPError as e:
                print(f"  [Mirror {idx}] {e.code} FAILED -> {url}")
            except Exception as e:
                print(f"  [Mirror {idx}] TIMEOUT/ERR ({e}) -> {url}")

audit_file("discovery/manifests/windows.json")
audit_file("discovery/manifests/macos.json")
