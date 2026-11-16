# Pulse engineering audit

## Architecture and repairs

The existing collector → engine → synchronous MetricsSink → TUI/headless design
was preserved, with direct proc parsing and the two allowed direct TUI dependencies.
No Prometheus service, kernel probe, privileged operation, or new product scope
was added.

- Removed guest/guest_nice double counting in aggregate CPU totals.
- Reject malformed CPU/process counters and invalid memory totals; check RSS and
  memory-conversion overflow. Cap each proc-file read at 4 MiB; check PID identity
  against its directory. Permission/vanished-process handling remains best effort.
- Validate CLI/config timing, history, frequency, thresholds, modes and arguments
  before creating timers/files; handle help successfully. Strict JSON rejects
  unknown keys and trailing documents; partial alert configs retain defaults.
- Bounded headless runs return errors instead of retrying collection forever.
- Engine copies snapshot slices, dispatches outside its state lock, logs sink
  errors, and releases the lock before closing sinks. Regression tests verify a
  sink can read the engine and cannot mutate stored state.
- Process history honors configured retention; reported sample interval reflects
  elapsed sampling time. TUI waits for a collection result before scheduling the
  next tick, propagates cancellation, and reports collection errors to the CLI.
- Ctrl+C works in filter mode; Unicode backspace preserves valid text. Shrinking
  process lists reset stale scroll offsets. Removed the unverified v1.0 TUI label.
- New alert logs use 0600. The engine owns sink closure.
- Canonical GitHub module/import paths; Go 1.24 minimum matches actual dependency
  requirements. Added full CI and corrected prerequisites, formulas, license,
  performance claims, security, commands, environment support, and roadmap.

## Results

`go test -race -count=1 ./...` passes all six packages. New regression cases cover
CPU accounting, malformed input, partial configs, invalid CLI flags, bounded
headless failures, snapshot isolation, callback reentry, Unicode and interrupts.

The built static binary passes fixture --once, --once --json, --enable-pss and
bounded --headless examples. Fixture JSON was parsed and checked for tick 2 and
three processes. A live /proc --once --enable-pss smoke run exited successfully;
live command-line output is deliberately kept private. A real TUI recording uses
only checked-in synthetic fixtures. `file` reports a statically linked ELF amd64
binary; `ldd` reports not a dynamic executable (its expected nonzero exit).

## Remaining limitations

No license selected yet; the prior README MIT badge was unsupported and removed.
No LICENSE or Pulse release archive will be published without owner confirmation.
The desired GitHub name has not been verified available. Source is prepared on main.

Only this Linux amd64 host is tested. No multihour retention/overhead test was
performed. Optional UID reads can fall back to 0; collection is not atomic across
files, so within-scan PID reuse can mix auxiliary metadata. PSS scans all readable
processes when enabled. CPU accounting is not cgroup quota accounting. Sinks/logging
are synchronous, and logs do not rotate. Prometheus remains a roadmap design.

Prior overbroad completion/performance assertions were replaced with current audit
status in PROJECT.md and PROGRESS.md. The README contains measured benchmark results.
GEMINI_HANDOFF_PROMPT.md is a local planning prompt and excluded from publication.

## Verification environment and limits

Audit date: 2026-11-15. Pop!_OS 24.04, Linux amd64, AMD Ryzen 7 7700,
Go 1.27.3. Commands use `GOCACHE=/tmp/portfolio-go-cache` because the default
cache is read-only in this sandbox. No security restrictions were disabled.

Formatting, `go vet ./...`, `go build ./...`, static `CGO_ENABLED=0` binary
builds, and `go mod verify` passed. Workflow YAML parsed with PyYAML; job and
trigger structure was inspected. `actionlint` is unavailable. Minimum Go versions
and remote GitHub Actions have not been executed here. CI retains full tests.

GitHub CLI authentication is invalid. Shell DNS/network access to GitHub and the
module proxy is blocked. Public web profile review was possible, but repository
name availability, current remote refs/releases, vulnerability advisories, and
remote CI cannot be verified. No pushes, repository creation, tags, or release
publication were performed.

A heuristic scan of all working files (including ignored files) and reachable Git
history blobs found no credential-pattern matches. It covers private-key headers,
common GitHub/AWS/service token formats, and quoted credential assignments. This
is not a guarantee of absence. Dedicated gitleaks/govulncheck tools are unavailable.
The final local manifest and scan report are in the sibling publication review
folder. Git author identity uses the existing user configuration and includes its
configured email; it has not been changed or fabricated.
