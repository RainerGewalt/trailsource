# Contributing

Thanks for taking the time. TrailSource is small and opinionated; the notes
below keep it that way.

## Before you start

- Open an issue for anything larger than a bug fix so the scope is agreed
  before code exists.
- Protocol drivers belong behind a narrow boundary (see
  `adapters/ethernetip/main.py` `read_tags()` and `internal/opcua`). Vendor
  SDKs, licences and platform constraints must not leak into the record
  mapping.
- The record format is specified outside this repository. Changes to sealing,
  hashing or verification need new entries in `testdata/vectors.json` that
  both the Go and the Python implementation reproduce.

## Development

```sh
make ci          # gofmt check, go vet, go test, Python unit tests, compose config
make demo        # local stack with docker compose
make verify      # verify the journals the demo wrote
```

Go code is formatted with `gofmt`. Python code targets 3.9+ and must stay
stdlib-only inside `adapters/pylib/trailsource_evidence`.

## Commits and pull requests

- Commit messages: `type(scope): subject` in English, imperative mood
  (`feat(opcua): ...`, `fix(evidence): ...`, `docs: ...`).
- One logical change per pull request. Include tests for behaviour changes.
- Do not commit credentials, certificates, journals or state files. The
  `.demo/` directory is ignored for that reason.
- Do not add domain-specific company, site or customer names to simulators,
  configs or docs. The demo domain is a generic extrusion line.

## Licence

By contributing you agree that your contributions are licensed under the
Apache License 2.0 that covers this project.
