//go:build linux && arm64

package exescroll

import _ "embed"

//go:embed binary/exe-scroll-linux-arm64
var embeddedBinary []byte
