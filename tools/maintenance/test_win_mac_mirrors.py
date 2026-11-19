import json
import time
import urllib.request

class RedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return urllib.request.Request(newurl, headers=req.headers)

opener = urllib.request.build_opener(RedirectHandler)

for manifest_file in ["discovery/manifests/windows.json", "discovery/manifests/macos.json"]:
    print(f"\nAUDITING: {manifest_file}")
    print(f"{'ID':<28} | {'STATUS':<10} | {'SPEED (MB/s)':<12} | {'TARGET URL'}")
    print("-" * 85)
    
    try:
        with open(manifest_file) as f:
            data = json.load(f)
    except Exception as e:
        print(f"Error opening {manifest_file}: {e}")
        continue

    for item in data:
        name = item.get("id", "unknown")
        mirrors = item.get("mirrors", [item.get("download_url", "")])
        
        for url in mirrors:
            if not url.startswith("http"):
                print(f"{name:<28} | LOCAL/OTHER| N/A          | {url}")
                continue
                
            req = urllib.request.Request(url, headers={'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)'})
            start = time.time()
            downloaded = 0
            status = "FAILED"
            speed = 0.0
            
            try:
                with opener.open(req, timeout=12) as resp:
                    chunk_size = 64 * 1024
                    while downloaded < 5 * 1024 * 1024:
                        chunk = resp.read(chunk_size)
                        if not chunk:
                            break
                        downloaded += len(chunk)
                    
                    elapsed = time.time() - start
                    if elapsed > 0:
                        speed = (downloaded / (1024 * 1024)) / elapsed
                    status = "OK" if downloaded > 0 else "EMPTY"
            except Exception as e:
                status = f"ERR ({e.__class__.__name__})"
                
            print(f"{name:<28} | {status:<10} | {speed:<12.2f} | {url[:42]}...")
