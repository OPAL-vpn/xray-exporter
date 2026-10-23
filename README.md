# xray-exporter

A Prometheus exporter that measures what a set of Xray client configs actually
do from one machine. Every config is started in-process, as an xray-core
instance linked into this binary, and probed through its tunnel; the last cycle
is served at `/metrics`.

There is no xray binary in the image and no child process at runtime: xray-core
is a Go dependency, so the image is the exporter plus `geoip.dat` and
`geosite.dat` from a pinned Xray release.

## What it measures

For every config in the config directory (the filename stem is the `config`
label), once per `INTERVAL` (default 5 min):

- **HTTPS through the tunnel**, for every target in `targets.yaml`: status code,
  whether the certificate verified, time to first byte, total time. Requests go
  through a socks5 inbound the exporter injects into the config, so the bytes
  take the same path a user's traffic would.
- **Tunnel DNS**, for every target: an `A` query sent to a `dokodemo-door`
  inbound the exporter injects, which forwards to `dns_upstream`. Up means a DNS
  response arrived, any rcode.
- **DoH and DoT through the tunnel**, for every resolver in `targets.yaml` and
  only for the transports that resolver configures: "can this tunnel carry
  encrypted DNS", not "is local DNS censored".
- **Egress verification** at start and on reload: whether the config's traffic
  actually leaves through the tunnel (see [Egress](#egress)).

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

To build the image from source instead (from the repository root):

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
no config was found, or when one failed to start, did not verify egress, or did
not finish its cycle.

## Configuration

All configuration is environment variables; the defaults are what the image
sets.

| Env | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:9787` | metrics listener |
| `XRAY_CONFIG_DIR` | `/config/xray` | config directory, one JSON per config |
| `TARGETS_FILE` | `/config/targets.yaml` | targets, resolvers, egress URL |
| `INTERVAL` | `5m` | cycle period |
| `WORKERS` | `8` | concurrent probes per config |
| `PROBE_TIMEOUT` | `10s` | per HTTP/DNS probe |
| `RESOLVER_TIMEOUT` | `5s` | per DoH/DoT probe |
| `EGRESS_CACHE_TTL` | `1h` | how long the direct baseline IP is reused |
| `XRAY_LOG_LEVEL` | `warning` | xray's own verbosity |
| `XRAY_LOCATION_ASSET` | `/usr/local/share/xray` | geo asset directory |
| `XRAY_LOG_FILE` | unset | mirror xray's stream into one file |
| `LOG_LEVEL` | `info` | exporter's own log level |

`XRAY_LOCATION_ASSET` is already set in the image and already points at the
baked-in `geoip.dat`/`geosite.dat`; override it only if you mount your own.

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
| `xray_egress_verified` | config | 1 = traffic leaves through the tunnel |
| `xray_egress_ip_info` | config, ip | 1, the observed egress address |
| `xray_egress_timestamp_seconds` | config | last verification |
| `xray_config_running` | config | instance is up |
| `xray_config_info` | config, xray_version | 1 |
| `xray_cycle_timestamp_seconds` | config | last completed cycle |
| `xray_cycle_duration_seconds` | config | cycle wall time |
| `xray_cycle_targets_total` | config | targets attempted |
| `xray_exporter_config_reload_total` | — | counter |
| `xray_exporter_config_reload_errors_total` | — | counter |
| `xray_build_info` | version, go_version | 1 |

Every expected series exists from the first scrape, with `up` at 0 until a probe
runs. Latency series are absent until the first success — a 0 would chart as
infinitely fast and poison averages, so a gap is the honest value. A failed
probe sets its `up` gauge to 0, moves the timestamp forward and leaves the last
successful latency in place; staleness is visible through the timestamp. Series
disappear only when their config or target does.

## Egress

A config's own `routing` rules can send probe traffic out directly, in which case
the numbers describe the wrong path. To catch that, the exporter compares the
address an IP-echo endpoint (your `egress_check_url`) sees through the config's
tunnel with the address it sees on a **direct** request.

The direct request is the only traffic this process sends outside a tunnel:

- it is fetched once per process and cached for `EGRESS_CACHE_TTL` (default
  `1h`), so it is one request per TTL no matter how many configs exist;
- the request through the tunnel happens once per config at start and on reload;
- equal addresses mean routing went out directly: `xray_egress_verified{config}`
  is 0 and an ERROR line names the config. Probes still run — the values then
  describe what the config actually does, with the verdict visible beside them.

## Logs

The exporter logs to stdout as `slog` `key=value` lines: one line per failed
probe, one summary per cycle, one line per instance start, failure and reload.
Successful probes log at debug. `LOG_LEVEL` (default `info`) sets the threshold.

xray-core registers its log handler **globally**, so xray's stream is
process-wide by construction: with several configs in one process a line cannot
say which config emitted it. Attribution exists where the exporter can sequence
it — a failed instance start is logged with `config=<name>` and the tail of
xray's own error. A steady-state xray warning names no config. The mitigation is
operational: one container per config if attribution matters. `XRAY_LOG_LEVEL`
(default `warning`) filters xray's own verbosity and `XRAY_LOG_FILE`, when set,
mirrors the same stream into one file; rotation belongs to the host.

`SIGTERM` stops the cycle, closes every xray instance and exits.

## Dashboard

`deploy/grafana/dashboard.json` is importable as-is into any Grafana that has a
Prometheus datasource: every panel reads a `datasource` template variable, so no
UID is baked in and no `__inputs` prompt is needed. It ships `instance` →
`config` → `target` variables, all multi-select with `All` as the default.
See `deploy/grafana/README.md`.

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
approach and the egress-verification idea are derived from
`github.com/batonogov/xray-health-exporter` (MIT) — see `NOTICE`.
