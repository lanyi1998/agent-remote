//go:build !windows || !amd64

package runtimebundle

import "embed"

// Keep the shared runtime resolver code buildable without embedding the
// Windows runtime assets into non-Windows or unsupported Windows binaries.
var assets embed.FS
