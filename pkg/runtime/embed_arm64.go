//go:build standalone

package runtime

import _ "embed"

//go:embed assets/k3s-arm64
var runtimeBinary []byte
