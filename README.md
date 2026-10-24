# xray-exporter

A Prometheus exporter that measures what a set of Xray client configs actually
do from one machine. Every config is started in-process, as an xray-core
instance linked into this binary, and probed through its tunnel; the last cycle
is served at `/metrics`.

## What it measures

For every config in the config directory (the filename stem is the `config`
label), once per `INTERVAL` (default 5 min):

- **HTTPS through the tunnel**, for every target: status, certificate verdict,
  TTFB and total time. Requests use an injected socks5 inbound, so they take a
  user's path.
- **Tunnel DNS**, for every target: an `A` query through an injected
  `dokodemo-door` inbound forwarded to `dns_upstream`. Up means any DNS response.
- **DoH and DoT through the tunnel**, for each configured resolver transport:
  can this tunnel carry encrypted DNS.

## Quick start

```sh
cd deploy
mkdir -p xray
cp ../examples/targets.yaml targets.yaml      # then edit: no targets are built in
cp /path/to/*.json xray/                      # one full xray client config per file
docker compose up -d
curl -s localhost:9787/metrics | grep '^xray_'
```

A fresh deployment measures nothing until `targets.yaml` exists and at least one
config JSON is in `xray/`.

`docker compose` pulls the released image. Without compose, run the same image
from the directory holding `xray/` and `targets.yaml`:

```sh
docker pull ghcr.io/opal-vpn/xray-exporter:v0.1.0
docker run --rm \
  -p 9787:9787 \
  -v "$PWD/xray:/config/xray:ro" \
  -v "$PWD/targets.yaml:/config/targets.yaml:ro" \
  ghcr.io/opal-vpn/xray-exporter:v0.1.0
```

Releases are multi-arch (`linux/amd64`, `linux/arm64`); `latest` follows the
newest release, so pin a tag where reproducibility matters. Tags and digests
are on the [releases page](https://github.com/OPAL-vpn/xray-exporter/releases).

### Building from source

To build the image yourself, from the repository root:

```sh
docker build -f deploy/Dockerfile -t xray-exporter:dev .
docker run --rm xray-exporter:dev --version
docker run --rm \
  -p 9787:9787 \
  -v "$PWD/deploy/xray:/config/xray:ro" \
  -v "$PWD/deploy/targets.yaml:/config/targets.yaml:ro" \
  xray-exporter:dev
```

`xray-exporter --once [--out FILE] [--deadline 5m]` skips the listener and the
timer, runs one cycle, prints a human-readable table and exits non-zero when
no config was found, or when one failed to start or did not finish its cycle.

## Configuration

All configuration is environment variables; the defaults are what the image
sets.

| Env | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:9787` | metrics listener |
| `XRAY_CONFIG_DIR` | `/config/xray` | config directory, one JSON per config |
| `TARGETS_FILE` | `/config/targets.yaml` | targets and resolvers |
| `INTERVAL` | `5m` | cycle period |
| `WORKERS` | `8` | concurrent probes per config |
| `PROBE_TIMEOUT` | `10s` | per HTTP/DNS probe |
| `RESOLVER_TIMEOUT` | `5s` | per DoH/DoT probe |
| `XRAY_LOG_LEVEL` | `warning` | xray's own verbosity |
| `XRAY_LOCATION_ASSET` | `/usr/local/share/xray` | geo asset directory |
| `XRAY_LOG_FILE` | unset | mirror xray's stream into one file |
| `LOG_LEVEL` | `info` | exporter's own log level |

`XRAY_LOCATION_ASSET` points at the image's baked-in `geoip.dat`/`geosite.dat`;
override it only to mount your own.

## Metrics

Namespace `xray_`. Identity of the vantage point comes from Prometheus
(`instance` and any static labels in the scrape config); the exporter adds no
self-labelling.

| Metric | Labels | Meaning |
|---|---|---|
| `xray_probe_up` | config, target | got an HTTP response |
| `xray_probe_http_status` | config, target | status code, 0 if none |
| `xray_probe_tls_ok` | config, target | certificate verified |
| `xray_probe_ttfb_seconds` | config, target | time to first byte |
| `xray_probe_total_seconds` | config, target | full request |
| `xray_target_timestamp_seconds` | config, target | DNS+HTTP pair completed |
| `xray_dns_up` | config, target | tunnel DNS answered |
| `xray_dns_lookup_seconds` | config, target | DNS round trip |
| `xray_resolver_up` | config, resolver, transport | doh / dot reachable |
| `xray_resolver_lookup_seconds` | config, resolver, transport | resolver round trip |
| `xray_resolver_timestamp_seconds` | config, resolver, transport | last attempt |
| `xray_config_running` | config | instance is up |
| `xray_config_info` | config, xray_version | 1 |
| `xray_cycle_timestamp_seconds` | config | last completed cycle |
| `xray_cycle_duration_seconds` | config | cycle wall time |
| `xray_cycle_targets_total` | config | targets attempted |
| `xray_exporter_config_reload_total` | — | counter |
| `xray_exporter_config_reload_errors_total` | — | counter |
| `xray_build_info` | version, go_version | 1 |

Every expected series exists from the first scrape with `up` at 0. Latency
series appear only after a success — a 0 would chart as infinitely fast and
poison averages. A failed probe sets `up` to 0, moves the timestamp forward and
keeps the last successful latency, so staleness is visible through the
timestamp. Series disappear only with their config or target.

## No direct traffic

The exporter sends no traffic outside a tunnel. HTTP and TLS probes, DoH, DoT
and the UDP DNS probe all reach their peers through an xray instance's
loopback inbound, so a config's `routing` decides the path. Only loopback is
dialed directly: the SOCKS port and DNS inbound the exporter starts, and the
free-port probe.

A tunnel that is down therefore surfaces as failed probes and
`xray_config_running` 0, not as measurements from a direct path.

## Logs

The exporter logs `slog` `key=value` lines to stdout: failed probes, one cycle
summary, instance start/failure/reload. Successful probes log at debug;
`LOG_LEVEL` (default `info`) sets the threshold.

xray registers its log handler **globally**, so with several configs in one
process a line cannot name its config. A failed start is attributed with
`config=<name>` and xray's error; steady-state warnings are not. Run one
container per config if attribution matters. `XRAY_LOG_LEVEL` filters xray's
verbosity; `XRAY_LOG_FILE` mirrors the stream to one file. That file is trimmed
in place once it passes `XRAY_LOG_MAX_BYTES` (default 1 GiB), keeping the newest
`XRAY_LOG_KEEP_BYTES` (default 750 MiB) from a whole line; set
`XRAY_LOG_MAX_BYTES=0` to leave the file unbounded and rotation to the host.
`SIGTERM` stops the cycle, closes instances and exits.

## Dashboard

`deploy/grafana/dashboard.json` imports as-is into any Grafana with a Prometheus
datasource: every panel reads a `datasource` variable, so no UID is baked in. It
ships `instance` → `config` → `target` variables, all multi-select with `All` as
the default. See `deploy/grafana/README.md`.

## Tests

Both suites bind loopback ports only and contact no remote host. The
end-to-end checks additionally drive the whole app entry point, so they sit
behind the `integration` build tag and CI runs both:

```sh
go test ./...                    # package tests
go test -tags integration ./...  # adds the end-to-end checks
```

## Licence

xray-exporter is licensed under the MIT Licence — see `LICENSE`.

xray-core is licensed under MPL-2.0 and is linked into this binary as a Go
dependency; the image contains no separate xray executable. The injection
approach is derived from `github.com/batonogov/xray-health-exporter` (MIT) —
see `NOTICE`.
