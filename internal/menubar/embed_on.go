//go:build menubar

package menubar

import _ "embed"

// helper is MenuBar.swift built for arm64 and x86_64 by scripts/build-menubar.sh.
//
//go:embed menubar.bin
var helper []byte
