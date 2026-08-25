//go:build linux && amd64

package exescroll

import _ "embed"

//go:embed binary/exe-scroll-linux-amd64
var embeddedBinary []byte
