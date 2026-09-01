//go:build windows && amd64

package runtimebundle

import "embed"

// assets contains the Windows runtime bundled into Windows builds only.
//
//go:embed all:assets
var assets embed.FS
