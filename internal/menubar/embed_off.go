//go:build !menubar

package menubar

// helper is empty in builds without the menubar tag; scripts/build-release.sh adds it.
var helper []byte
