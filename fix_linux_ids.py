import json

path = 'discovery/manifests/linux.json'
with open(path, 'r') as f:
    entries = json.load(f)

for item in entries:
    if 'id' not in item or not item['id']:
        # Generate a slug ID like kali-gui-amd64, ubuntu-tty-amd64
        distro_slug = item['distro'].lower().replace(' ', '-').replace('(', '').replace(')', '').replace('/', '-')
        flavor = item.get('flavor', 'gui')
        arch = item.get('arch', 'amd64')
        item['id'] = f"{distro_slug}-{flavor}-{arch}"
    
    # Ensure standard top-level categorization
    if not item.get('category'):
        item['category'] = "Linux Distributions"

with open(path, 'w') as f:
    json.dump(entries, f, indent=2)

print(f"=> Injected IDs and verified {len(entries)} Linux entries successfully!")
