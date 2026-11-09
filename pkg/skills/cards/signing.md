---
name: image-signing
description: Sign images and make the cluster run only signed, digest-pinned images
tags: [security, signing, images, supply-chain]
danger: false
---
1) Key pair (password from the prompt or ORCINUS_SIGNING_PASSWORD):
    orcinus sign keygen            # ~/.orcinus/keys/default.key/.pub
2) Sign an image already pushed (docker login first; signs the digest the tag points to now):
    orcinus sign image registry.example.com/app:1.0
3) Trust the public key in the cluster, report first, then enforce:
    orcinus sign trust add default
    orcinus sign enforce --mode audit      # installs Kyverno on first use
    orcinus sign status                    # unsigned running images; sign or exempt them
    orcinus sign enforce --mode enforce
Exempt what you cannot sign: orcinus sign exempt add <namespace|image-prefix>
CI: ORCINUS_SIGNING_KEY=<key PEM> ORCINUS_SIGNING_PASSWORD=… orcinus sign image $IMAGE
With signing on, `orcinus deploy` refuses unsigned images before applying, and pins
signed ones to @sha256 digests. Break-glass: orcinus sign enforce --mode off.
Docs: docs/SIGNING.md.
