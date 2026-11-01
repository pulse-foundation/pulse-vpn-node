#!/usr/bin/env bash
# Full inventory is advisory; owned builds also gate fixable OS High/Critical findings.
set -euo pipefail

test "$#" -eq 3
image=$1
report=$2
mode=$3
case "$mode" in
  built-image|upstream-image) ;;
  *) echo 'Expected built-image or upstream-image scan mode' >&2; exit 2 ;;
esac
mkdir -p "$(dirname "$report")"

# Never filter the saved inventory. A scanner/registry/database failure is fatal.
trivy image --scanners vuln --format json --exit-code 0 --timeout 10m \
  --output "$report.json" "$image"
trivy convert --format table --severity HIGH,CRITICAL --output "$report.txt" "$report.json"
cat "$report.txt"

findings=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH" or .Severity == "CRITICAL")] | length' "$report.json")
if [ "$findings" -gt 0 ]; then
  echo "::warning title=Image security findings::$image has $findings High/Critical findings. Review the security-reports artifact; upstream binary findings are advisory."
fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    printf '\n### Image security: `%s`\n\n' "$image"
    printf 'High/Critical findings: **%s**. Full inventory and readable reports are in the security-reports artifact.\n\n' "$findings"
    printf 'Policy: `%s`. Upstream findings require review; see the complete report even when CI passes.\n' "$mode"
  } >> "$GITHUB_STEP_SUMMARY"
fi

# A fixed distro package does not mean the vendor published a fixed service image.
if [ "$mode" = upstream-image ]; then
  exit 0
fi

# Use Trivy's package classification and vendor fix information, not a CVE allowlist.
status=0
trivy image --scanners vuln --pkg-types os --ignore-unfixed --severity HIGH,CRITICAL \
  --exit-code 1 --timeout 10m --output "$report.os-gate.txt" "$image" || status=$?
if [ -f "$report.os-gate.txt" ]; then
  cat "$report.os-gate.txt"
fi
exit "$status"
