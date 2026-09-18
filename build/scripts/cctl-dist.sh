#!/bin/sh
#
# Cross-compile cctl for every platform a release ships, and write the archives
# and their checksums into ${OUTPUT_DIR}/cli.
#
# A script rather than a mise task body so that the release workflow and a
# developer's `mise run cctl-dist` run the same code. The workflow has Go but
# no mise -- installing mise there would pull Hugo and Node to build one
# binary -- and two copies of this loop would drift the day one of them gained
# a platform.
set -eu

: "${OUTPUT_DIR:=$(pwd)/output}"

# Same version derivation as the `build` task: git describe keeps the v from
# the tag, and the image tag does not carry one, so a binary built from a
# checkout and one built by the release workflow report the same version
# instead of differing by a prefix.
version="${VERSION:-}"
if [ -z "${version}" ]; then
	version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
	version="${version#v}"
fi
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"

dist="${OUTPUT_DIR}/cli"
rm -rf "${dist}"
mkdir -p "${dist}"

# The platforms an operator runs cctl on, which is not the platform a node
# runs. Windows is absent because it has never been run once, not because it
# was ruled out -- claiming support for something nobody has started is how a
# download page stops being worth believing.
for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os="${platform%/*}"
	arch="${platform#*/}"
	staging="$(mktemp -d)"

	# CGO off, so the linux builds are static. cctl is copied onto whatever
	# machine an operator happens to have, and a binary that needs a matching
	# libc is a binary that works until it does not.
	CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" \
		go build -trimpath \
		-ldflags "-s -w -X main.version=${version} -X main.commit=${commit}" \
		-o "${staging}/cctl" ./cmd/cctl

	cp LICENSE "${staging}/LICENSE"

	# The name carries the OS and the architecture in the shape the usual
	# installers look for, so `mise use ubi:Corium-OS/Corium[exe=cctl]` picks
	# the right archive without being told which.
	tar -czf "${dist}/cctl_${version}_${os}_${arch}.tar.gz" -C "${staging}" cctl LICENSE
	rm -rf "${staging}"
done

# One checksum file, rather than one signature per archive: the release signs
# this, and a signature over it covers every binary underneath. sha256sum on
# Linux, shasum on macOS -- this script runs on both.
if command -v sha256sum >/dev/null 2>&1; then
	sum="sha256sum"
else
	sum="shasum -a 256"
fi

# cd so the file names it records are bare, which is what `-c` expects to find
# beside it after a download.
(cd "${dist}" && ${sum} ./*.tar.gz | sed 's| \./| |' > SHA256SUMS)

ls -l "${dist}"
