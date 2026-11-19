# Phase 3 "Tier 2": a hermetic, reproducible toolset for inspecting a
# catalog ISO's on-disk layout without booting it. Pinned to a specific
# Debian release (not :latest) so this image's tool versions don't drift
# out from under the CI pipeline between runs.
FROM debian:12.7-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    xorriso \
    squashfs-tools \
    genisoimage \
    && rm -rf /var/lib/apt/lists/*

COPY verify-iso-structure.sh /usr/local/bin/verify-iso-structure.sh
RUN chmod +x /usr/local/bin/verify-iso-structure.sh

ENTRYPOINT ["/usr/local/bin/verify-iso-structure.sh"]
