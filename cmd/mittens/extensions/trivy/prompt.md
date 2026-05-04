# Trivy Vulnerability Scanner

Trivy is installed as a native binary. Use it to scan Docker images, filesystems, and repositories for CVEs.

## Quick Start

```bash
# Scan a Docker image
trivy image <image-name>

# Scan with JSON output (for parsing)
trivy image --format json <image-name>

# Scan only HIGH and CRITICAL
trivy image --severity HIGH,CRITICAL <image-name>

# Scan the current filesystem
trivy fs .
```

## Vulnerability Triage Workflow

1. **Discover Dockerfiles**: `find . -name "Dockerfile*" -type f | grep -v node_modules`
2. **Build**: `docker build -f ./path/to/Dockerfile <context> -t <name>-test`
3. **Scan**: `trivy image --format json <name>-test`
4. **Triage each CVE**: Fix by upgrading the package, or ignore with expiry in `.trivyignore`
5. **Re-scan**: Rebuild and re-scan to verify fixes

## Ignoring CVEs

Add to `.trivyignore` with an expiry date comment:

```
# exp:2026-05-13
CVE-2024-12345
```

Suggested SLA for expiry dates:
- Critical/High: 30 days
- Medium: 60 days
- Low: 90 days

## Vulnerability Database

The Trivy DB is cached at `~/.cache/trivy` and updates automatically on first scan. Subsequent scans reuse the cached DB unless it's stale (>24h).
