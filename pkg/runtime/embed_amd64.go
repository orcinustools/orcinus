//go:build standalone

package runtime

import _ "embed"

//go:embed assets/k3s
var runtimeBinary []byte
