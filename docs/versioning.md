# Versioning

This project follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).
Releases are annotated tags of the form `vMAJOR.MINOR.PATCH`. That exact string
is what `release.yml` builds from, what `--version` prints, and what lands in
`xray_build_info{version="..."}`.

The history follows [Conventional Commits](https://www.conventionalcommits.org/),
so the commit types say which part of the version moves.

## What counts as the public API

The "API" of a long-running exporter is what operators and dashboards depend on:

- metric names, label sets, types and units in the `xray_` namespace, and the
  exposition at `/metrics`
- the `/healthz` contract: `200` with `ok` while the process serves
- environment variables and their defaults, as listed in the README's
  configuration table
- the `targets.yaml` schema: `egress_check_url`, `dns_probe_domain`,
  `dns_upstream`, `targets[].host`, `dns_resolvers[].name` with `doh_url` or
  `dot_host`
- CLI flags (`--once`, `--out`, `--deadline`, `--version`) and the shape of the
  `--once` report
- the image name `ghcr.io/opal-vpn/xray-exporter`, its tag scheme, and the
  supported platforms (`linux/amd64`, `linux/arm64`)

Everything under `internal/` is private by construction: no other module can
import it, so it moves in any release without a version bump of its own.

## Which number moves

| Change | Version |
|---|---|
| `fix:` — a corrected behaviour that already matched the contract | PATCH |
| `feat:` — a new metric, env var, flag or accepted config key | MINOR |
| `!` or a `BREAKING CHANGE:` footer touching the list above | MAJOR |
| `docs:`, `test:`, `refactor:`, `chore:`, `ci:` alone | no release on their own; they ride the next one |

Removing or renaming a metric, changing a label set, adding a required config
key, or changing a default in a way that alters what an existing deployment
measures is a MAJOR change. Adding a metric or an optional key is MINOR. Filling
in a series that was silently missing is PATCH.

## Before 1.0.0

The project is at `v0.1.0`. SemVer §4 applies: in `0.y.z` anything may change
and a MINOR bump may carry a breaking change. Here that means a `0.y.0` may
rename a metric, a label or an env var, and the tag annotation says what broke.

`1.0.0` is cut when the metric and configuration surfaces are the ones worth
being stuck with — after the shapes in `docs/superpowers/specs/` have survived
real deployments rather than on a schedule.

## Releasing

1. Merge the work with Conventional Commit messages.
2. Take the bump from the commit types, or from the change to the public API
   when the two disagree.
3. Tag and push:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z" -m "what changed that a deployment would notice"
   git push origin vX.Y.Z
   ```

4. `release.yml` builds the multi-arch image and pushes `:vX.Y.Z` and `:latest`.
5. Once the image job is green, publish the release entry:

   ```bash
   gh release create vX.Y.Z --title vX.Y.Z --notes-file notes.md --latest
   ```

   The notes carry what a deployment would notice, the supported platforms and
   the multi-arch manifest digest from the build log (the
   `pushing manifest for …:vX.Y.Z@sha256:…` line), so the pin is discoverable
   without opening the registry.

The `v` prefix is load-bearing: the workflow triggers on `v*` only, so a tag
without it produces no image. The tag alone does **not** create an entry on the
Releases page — only step 5 does — so skipping it publishes an image that nobody
browsing releases can find.

There is no `CHANGELOG.md`. The GitHub Release body is the changelog for a
version, and the annotated tag repeats its summary, so steps 3 and 5 are both
written by hand. `release.yml` only builds the image.

## Tags on the image

`:latest` follows the newest release, whatever its major. Pin `:vX.Y.Z`, or the
digest it resolves to, wherever a rollover would be a surprise — the README's
install snippet pins a tag for exactly that reason, and each release body records
the manifest digest that tag resolved to when it was published.

## What the version does not describe

- **xray-core.** It is pinned in `go.mod` and versioned independently. The
  running build per config is exported as `xray_config_info{config, xray_version}`.
- **The Go toolchain.** Reported in `xray_build_info{go_version}`.
- **Local builds.** `go build` without `-ldflags -X main.version` reports `dev`;
  the image build passes the tag through the `VERSION` build arg.
- **The Go module major.** This is a service, not a library, so the module path
  never takes a `/vN` suffix; a major release is distinguished by the image tag
  alone and costs nothing to import.
