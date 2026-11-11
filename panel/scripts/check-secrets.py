#!/usr/bin/env python3
"""Scan owned source files, respecting Git's ignored local tools and state."""
from pathlib import Path
import shutil
import subprocess
import tempfile

root = Path(__file__).resolve().parents[2]
paths = subprocess.check_output(
    ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "panel", "examples"],
    cwd=root,
).split(b"\0")
with tempfile.TemporaryDirectory(prefix="panel-secrets-") as temporary:
    scan_root = Path(temporary)
    for raw_path in set(paths):
        if not raw_path:
            continue
        relative = Path(raw_path.decode())
        source = root / relative
        if source.is_symlink() or not source.is_file():
            continue
        destination = scan_root / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
    subprocess.run(
        [str(root / "panel/bin/gitleaks"), "dir", "--redact", "--no-banner", "--timeout", "60", str(scan_root)],
        check=True,
    )
