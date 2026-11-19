.PHONY: build vet test crosscheck check

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

# crosscheck builds and vets this repo for every non-native platform/arch
# it ships for (GOOS=windows amd64, GOOS=darwin amd64/arm64, GOOS=linux
# arm64) — see scripts/crosscheck.sh for why this exists: a native
# `go build ./...` on Linux never compiles the windows/darwin-gated
# files at all, so it cannot catch a mistake (such as an unused import)
# that only shows up when those files actually get compiled.
crosscheck:
	./scripts/crosscheck.sh

# check is the full local gate: the native build/vet/test every commit
# must pass, plus crosscheck so a cross-platform-only break is caught
# before CI does.
check: build vet test crosscheck
