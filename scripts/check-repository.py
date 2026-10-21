#!/usr/bin/env python3
"""Reject tracked build products and non-placeholder example credentials.

Only filenames/key names are reported; credential values are never printed.
This narrow guard is not a substitute for a full history secret scan.
"""
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT).decode().split("\0")
errors = []
for name in filter(None, tracked):
    p = Path(name)
    if (name.startswith("cloudnest_go/dist/") or name == "cloudnest_go/main"
            or p.suffix == ".o" or "xcuserdata" in p.parts or p.name == ".DS_Store"):
        errors.append(f"generated artifact is tracked: {name}")
    if p.name == ".env" or (p.name.startswith(".env.") and p.name != ".env.example"):
        errors.append(f"private environment file is tracked: {name}")

example = ROOT / "cloudnest_go/.env.example"
safe_values = {
    "MYSQL_DSN": "cloudnest:CHANGE_ME@tcp(127.0.0.1:3306)/cloudnest?charset=utf8mb4&parseTime=True&loc=Local",
    "REDIS_PASSWORD": "",
    "JWT_SECRET": "replace_with_a_strong_random_secret",
}
for line in example.read_text().splitlines():
    key, sep, value = line.partition("=")
    if sep and key in safe_values and value != safe_values[key]:
        errors.append(f"non-placeholder example credential: {key}")

if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("Repository hygiene checks passed (tracked paths and example credentials).")
