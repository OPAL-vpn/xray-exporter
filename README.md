# xray-exporter

A Prometheus exporter that measures what a set of Xray client configs actually
do from one machine. Every config is started in-process, as an xray-core
instance linked into this binary, and probed through its tunnel; the last cycle
is served at `/metrics`.

## What it can measure

For every config in the config set (a filename stem, or the panel's label;
see [Config sources](#config-sources)), once per `INTERVAL` (default 5 min):

- **HTTPS through the tunnel**, for every target: status, certificate verdict,
  TTFB and total time. Requests use an injected socks5 inbound, so they take a
  user's path.
- **Tunnel DNS**, for every target: an `A` query through an injected
  `dokodemo-door` inbound forwarded to `dns_upstream`. Up means any DNS response.
- **DoH and DoT through the tunnel**, for each configured resolver transport:
  can this tunnel carry encrypted DNS.
- **Controls**, once per cycle: an `https` GET, a `tcp` connect or a `dns`
  lookup on each named host.
- **Entry connects**, with `entry_tcp`: a direct TCP connect to the first
  outbound address of each running config.
- **The egress network**, once an hour: the AS name and country `ipinfo.io`
  reports for this host, aged by its own timestamp.

## Quick start

```sh
cd deploy
mkdir -p xray
cp ../examples/xray-exporter-config.yml .     # then edit: no targets are built in
cp /path/to/*.json xray/                      # one full xray client config per file
docker compose up -d
curl -s localhost:9787/metrics | grep '^xray_'
```

A fresh deployment measures nothing until `xray-exporter-config.yml` exists and
at least one config JSON is in `xray/` — or, with a
[Remnawave source](#config-sources) configured, until the panel returns one.

`docker compose` pulls the released image. Without compose, run the same image
from the directory holding `xray/` and `xray-exporter-config.yml`:

```sh
docker pull ghcr.io/opal-vpn/xray-exporter:latest
docker run --rm \
  -p 9787:9787 \
  -v "$PWD/xray:/config/xray:ro" \
  -v "$PWD/xray-exporter-config.yml:/config/xray-exporter-config.yml:ro" \
  ghcr.io/opal-vpn/xray-exporter:latest
```

See tags on the [releases page](https://github.com/OPAL-vpn/xray-exporter/releases).

### Building from source

To build the image yourself, from the repository root:

```sh
docker build -f deploy/Dockerfile -t xray-exporter:dev .
docker run --rm xray-exporter:dev --version
docker run --rm \
  -p 9787:9787 \
  -v "$PWD/deploy/xray:/config/xray:ro" \
  -v "$PWD/deploy/xray-exporter-config.yml:/config/xray-exporter-config.yml:ro" \
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
| `XRAY_CONFIG_DIR` | `/config/xray` | default directory for the directory source |
| `EXPORTER_CONFIG_FILE` | `/config/xray-exporter-config.yml` | targets, resolvers and the config source |
| `INTERVAL` | `5m` | cycle period |
| `WORKERS` | `8` | concurrent probes per config |
| `PROBE_TIMEOUT` | `10s` | per HTTP/DNS probe |
| `RESOLVER_TIMEOUT` | `5s` | per DoH/DoT probe |
| `CONTROL_TIMEOUT` | `5s` | per direct [reachability](#reachability) probe, controls and entry connects |
| `XRAY_LOG_LEVEL` | `warning` | xray's own verbosity |
| `XRAY_LOCATION_ASSET` | `/usr/local/share/xray` | geo asset directory |
| `XRAY_LOG_FILE` | unset | mirror xray's stream into one file |
| `LOG_LEVEL` | `info` | exporter's own log level |

`XRAY_LOCATION_ASSET` points at the image's baked-in `geoip.dat`/`geosite.dat`;
override it only to mount your own.

## Config sources

`xray-exporter-config.yml` names where the configs come from in one optional
block. Without it the exporter reads every `*.json` file in `XRAY_CONFIG_DIR`,
as it always has.

A directory, stated explicitly (the `path` defaults to `XRAY_CONFIG_DIR`):

```yaml
xray_config_source:
  type: directory
  path: /config/xray
```

A Remnawave panel, fetched every `refresh` (default `5m`; `0` fetches at startup
and on `SIGHUP` only):

```yaml
xray_config_source:
  type: remnawave
  url: https://panel.example.com/api/sub/<shortUuid>
  refresh: 5m
```

`url` is the subscription *API* URL: the exporter appends `/json` unless the
path already ends with it, and only the Xray JSON array form is accepted — a
mihomo or plain-text subscription is a fetch failure. `path` and `url` are
mutually exclusive, `refresh` is remnawave-only. The block replaces nothing else: `targets:`,
`dns_resolvers:` and `dns_probe_domain` keep their meaning for both sources.
/json`, a 20 s timeout and an 8 MiB body cap. No token and
no cookie are sent.

### The directory source is unchanged

It is read at startup and on `SIGHUP`, never on a timer. A startup with an
empty or unreadable directory is fatal. On `SIGHUP` a read error keeps the
previous set, counts a reload error and increments
`xray_exporter_config_fetch_errors_total`, while a readable empty directory is
authoritative and stops every config. Its reload errors keep their old meaning:
a config that fails to start makes the reload an error.

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
| `xray_exporter_config_fetch_total` | — | counter, fetch attempts |
| `xray_exporter_config_fetch_errors_total` | — | counter, fetches with no usable set |
| `xray_exporter_config_fetch_timestamp_seconds` | — | last successful fetch; absent until then |
| `xray_exporter_config_source_info` | type | 1, the configured source |
| `xray_build_info` | version, go_version | 1 |
| `xray_network_info` | as_name, country | 1, the network the host egresses from (ipinfo.io); [Reachability](#reachability) only |
| `xray_network_timestamp_seconds` | — | last successful egress lookup; absent until then |
| `xray_control_up` | kind, name, class | direct probe succeeded |
| `xray_control_seconds` | kind, name, class | last successful round trip |
| `xray_entry_tcp_up` | config | direct connect to the config's entry succeeded |
| `xray_entry_tcp_seconds` | config | last successful connect time |

Every expected series exists from the first scrape with `up` at 0. Latency
series appear only after a success — a 0 would chart as infinitely fast and
poison averages. A failed probe sets `up` to 0, moves the timestamp forward and
keeps the last successful latency, so staleness is visible through the
timestamp. Series disappear only with their config or target.

## Dashboard

`deploy/grafana/dashboard.json` imports as-is into any Grafana with a Prometheus
datasource: every panel reads a `datasource` variable, so no UID is baked in. It
ships `instance` → `config` → `target` variables, all multi-select with `All` as
the default. See `deploy/grafana/README.md`.

## Licence

xray-exporter is licensed under the MIT Licence — see `LICENSE`.

xray-core is licensed under MPL-2.0 and is linked into this binary as a Go
dependency; the image contains no separate xray executable. The injection
approach is derived from `github.com/batonogov/xray-health-exporter` (MIT) —
see `NOTICE`.
