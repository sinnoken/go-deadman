# Security Policy

## Supported versions

Only the latest release receives fixes.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through
[GitHub Security Advisories](https://github.com/sinnoken/go-deadman/security/advisories/new)
with steps to reproduce and the affected version (`go-deadman -version`).

You should get a response within 7 days.

## Verifying downloads

Every release ships `SHA256SUMS.txt`, and each binary has a GitHub build-provenance
attestation:

```bash
gh attestation verify go-deadman.exe --repo sinnoken/go-deadman
```
