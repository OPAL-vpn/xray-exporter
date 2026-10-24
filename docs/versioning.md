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
- the `targets.yaml` schema: `dns_probe_domain`, `dns_upstream`,
  `targets[].host`, `dns_resolvers[].name` with `doh_url` or `dot_host`
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
3. Tag and push, with the summary a deployment would notice in the annotation:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z" -m "XRAY_LOG_FILE is now bounded; set XRAY_LOG_MAX_BYTES=0 to opt out"
   git push origin vX.Y.Z
   ```

4. `release.yml` finishes it, in two jobs:
   - `image` builds and pushes `:vX.Y.Z` and `:latest` for `linux/amd64` and
     `linux/arm64`, and surfaces the multi-arch manifest digest as a job output.
   - `release`, once that succeeds, creates the GitHub Release: the tag's
     annotation body, then a `## Container image` block with the pull line, the
     platforms and the digest.

Pushing the tag is the only manual step, and the `v` prefix is load-bearing: the
workflow triggers on `v*`, so a tag without it publishes nothing. Re-running the
`release` job edits the existing entry rather than failing on the duplicate, so a
corrected note needs a re-run, not a new tag. The image job holds
`packages: write` and the release job holds `contents: write`; neither gets the
other's scope.

The annotation is the release body, so write it for whoever runs the image, not
for a reader of `git log`. Keep its first line to the version: the body is
published and the title already carries the tag. A signed tag keeps its
signature; only `contents:body` is read, so the signature never lands in the
notes.

There is no `CHANGELOG.md`. The annotation, and the Release body built from it,
are the changelog for a version.

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
