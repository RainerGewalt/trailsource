# Testing

How TrailSource is tested: which levels exist, what each one proves, and how
to run it. The `Makefile` is the entry point; this page explains what its
targets mean. The levels follow the testing concept of the Trail projects.

## Levels

**L1 Unit.** Logic decided from one function: the address-space
fingerprint, bindings and diff (`internal/inventory`), the mapping file and
its skeleton (`internal/mapping`), reason codes and the state model
(`internal/status`), configuration and `config_version`
(`internal/config`), JCS, sealing and verification rules
(`internal/evidence`), OPC UA value encoding and browse batching
(`internal/opcua`), certificate generation (`internal/certgen`). Go
`*_test.go` beside the code, plus the stdlib-only Python tests of the
preview evidence library (`adapters/pylib/tests`). `make test`; needs
nothing.

**L2 Component.** A unit with its real collaborators, in process: the trust
list against generated certificate chains with and without revocation lists
(`internal/trust`), the persistent chain position on temporary files
(`internal/chain`), the report against a fixture journal
(`internal/report`, `testdata/report`), the command line (`cmd/trailsource`).
`make test`; needs nothing.

**L3 Conformance.** TrailSource writes Trail Evidence Records and has to
agree with the reference:

- its own canonicalisation vectors (`testdata/vectors.json`), reproduced by
  the Go implementation and by the Python library;
- the reference vectors of trail-evidence (`TestReferenceVectors`): every
  valid vector within the producer role reproduces payload JCS,
  `content_hash`, core JCS and `chain_hash` byte for byte, a record with a
  legacy integer `seq` verifies as written, and every invalid vector fails
  TrailSource's verifier. Vectors that carry `profile` or `signature`, which
  TrailSource does not produce, are named in the test log;
- every reason code is in the trail-evidence catalog
  (`TestCodesAreInTheTrailEvidenceCatalog`);
- journals of real runs are checked with the reference verifier
  `trail-evidence verify` inside `make first-use`, when it is on `PATH`.

The first three read a trail-evidence checkout from `TRAIL_EVIDENCE_DIR`,
or from a checkout next to this repository; without one they are skipped
locally. CI checks out trail-evidence v0.2.1, the first tag whose catalog
lists `source.subscription_stalled`, and sets the variable, so there they
run and a missing file fails.

**L4 Scenario.** Product behaviour against a running OPC UA server:
`make scenarios` runs the seven scenarios of `docs/SCENARIOS.md` with the
bundled simulator, a local Mosquitto and the adapter as plain processes, and
each one checks its own outcome (records, verification, bindings).
`docs/INTEROP.md` records the runs against servers of other
implementations; those are manual.

**L5 First use.** `make first-use` (`scripts/first-use.sh`) walks through the
README and `docs/CONNECT_YOUR_SERVER.md` as a new user would and stops at the
first step that does not behave as documented: install, `make sim-venv`, the
seven scenarios, the report, the guide for your own server (played by the
bundled simulator, including the documented failure of an unchanged example
configuration and a tampered copy of the journal), the Docker demo stack, and
the reference verifier.

- `make first-use` checks the committed `HEAD` from a fresh clone, so files
  that exist only in a working copy cannot help. This is the check before a
  tag.
- `make first-use RELEASE=0.1.1` checks what was published: the tag from
  GitHub, `go install …@v0.1.1`, the release archive against
  `checksums.txt`, and the GHCR image, each of which must report `0.1.1`;
  the demo stack then runs the published image.

It needs what the README asks of a user (git, Go 1.24 or newer, Mosquitto,
Python 3.9 or newer, Docker with Compose v2) and the free ports it lists at
the top. It removes its processes, containers and working directory
afterwards (`FIRST_USE_KEEP=1` keeps the directory).

No tag without a passing `make first-use` on the release commit; if the
check after the tag fails, a patch release follows.

## Demo-only permissions

The demo service `demo-init` runs as root and makes `.demo/state` and
`.demo/journal` writable for everyone, so that the adapters (uid 10001) can
write on any host. `scripts/check_demo_init.py`, run by `make ci` and in CI
on `docker compose config --format json`, fails when that service could
reach anything else: a mount outside `.demo/`, a recursive or optioned
chmod, a path that is not one of its mounts, or a network.

## Determinism

A test proves a property with a signal that is ordered with it, never by
waiting. The scenarios and `make first-use` wait for the record, port or
message they need (a `source.node_added` record, the retained health message
with `last_event`, the simulator's port); the time bounds only stop a hang
and are never the assertion.

## Every fix ships with a negative control

A test for a fix has to be shown to fail without it: remove or mutate the
code under test, run the test, record that it fails, restore the file and
check that it is identical.

Recorded controls:

- `scripts/check_demo_init.py`: an extra mount `./configs:/demo/configs`, a
  recursive `chmod -R`, an additional path `/etc` and the repository root as
  mount source each make it fail; the unchanged compose file passes.
- `TestReferenceVectors`: with `anchor_hash` left out of the core, the
  vector `chain-restart` fails on core JCS and `chain_hash`; restored, it
  passes.

## Commands

- `make test`: Go tests and the Python unit tests (L1, L2, L3 when a
  trail-evidence checkout is available).
- `make ci`: gofmt, vet, tests, Python compile, Compose configuration and
  the demo-init check.
- `make scenarios`: L4.
- `make first-use`, `make first-use RELEASE=x.y.z`: L3 to L5.

## CI

`.github/workflows/ci.yml` runs gofmt, build, vet and the Go tests with
trail-evidence v0.2.1 checked out, the Python compile and unit tests, and the
Compose configuration with the demo-init check, on every push and pull
request. The scenarios and `make first-use` are not part of CI: they need a
local Mosquitto, the simulator and Docker on fixed ports. They run by hand
before and after every tag.
