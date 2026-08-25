//go:build !(linux && (amd64 || arm64)) && !(darwin && (amd64 || arm64))

package exescroll

var embeddedBinary []byte
