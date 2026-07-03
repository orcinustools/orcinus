#!/usr/bin/env sh
# Orcinus standalone installer — downloads the self-contained `orcinus-standalone`
# binary (Kubernetes runtime built in; no container runtime needed; linux/amd64).
#
#   curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install-standalone.sh | sh
#
# It's a thin wrapper over install.sh with ORCINUS_STANDALONE=1. The same
# overrides apply:
#   ORCINUS_VERSION   release tag to install (default: latest)
#   ORCINUS_INSTALL   install directory       (default: /usr/local/bin)
set -eu

export ORCINUS_STANDALONE=1
curl -fsSL "https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh" | sh
