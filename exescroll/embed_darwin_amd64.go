//go:build darwin && amd64

package exescroll

import _ "embed"

//go:embed binary/exe-scroll-darwin-amd64
var embeddedBinary []byte
