Place the Windows amd64 runtime files under `windows_amd64` before building.

The Windows build currently supports amd64 only. Expected layout:

    windows_amd64/bin/busybox.exe

Additional DLLs and files are preserved with their relative paths. Files named
`_placeholder` are ignored.

The Windows worker invokes `busybox.exe sh -s` directly, so applet link files
are not generated at runtime.

Only Windows amd64 builds embed these files. Non-Windows builds do not include
the Windows runtime.
