Place runtime files in one or both architecture directories before building.

Expected Windows layout:

    windows_amd64/usr/bin/bash.exe
    windows_amd64/usr/bin/sh.exe
    windows_amd64/usr/bin/msys-2.0.dll
    windows_amd64/bin/busybox.exe

Use `windows_386` for the 32-bit build. Additional DLLs and files are preserved
with their relative paths. Files named `_placeholder` are ignored.
