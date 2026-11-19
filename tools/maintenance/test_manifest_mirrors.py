import json
import time
import urllib.request

class RedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return urllib.request.Request(newurl, headers=req.headers)

opener = urllib.request.build_opener(RedirectHandler)

with open("discovery/manifests/linux.json") as f:
    distros = json.load(f)

print(f"{'DISTRO ID':<33} | {'STATUS':<10} | {'SPEED (MB/s)':<12} | {'MIRROR'}")
print("-" * 85)

for d in distros:
    name = d.get("id", "unknown")
    mirrors = d.get("mirrors", [d.get("download_url", "")])
    
    for url in mirrors:
        if url.startswith("/") or not url.startswith("http"):
            print(f"{name:<33} | LOCAL FILE | N/A          | {url}")
            continue
            
        req = urllib.request.Request(url, headers={'User-Agent': 'Mozilla/5.0'})
        start = time.time()
        downloaded = 0
        status = "FAILED"
        speed = 0.0
        
        try:
            with opener.open(req, timeout=12) as resp:
                chunk_size = 64 * 1024
                while downloaded < 10 * 1024 * 1024:
                    chunk = resp.read(chunk_size)
                    if not chunk:
                        break
                    downloaded += len(chunk)
                
                elapsed = time.time() - start
                if elapsed > 0:
                    speed = (downloaded / (1024 * 1024)) / elapsed
                status = "OK" if downloaded > 0 else "EMPTY"
        except Exception as e:
            status = "ERR"
            
        print(f"{name:<33} | {status:<10} | {speed:<12.2f} | {url[:45]}...")
