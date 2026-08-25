//go:build darwin && arm64

package exescroll

import _ "embed"

//go:embed binary/exe-scroll-darwin-arm64
var embeddedBinary []byte
