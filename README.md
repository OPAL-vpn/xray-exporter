# xray-exporter

A Prometheus exporter that measures what a set of Xray client configs actually
do from one machine. Every config is started in-process, as an xray-core
instance linked into this binary, and probed through its tunnel; the last cycle
is served at `/metrics`.

## What it measures

For every config in the config set (a filename stem, or the panel's label;
see [Config sources](#config-sources)), once per `INTERVAL` (default 5 min):

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
docker pull ghcr.io/opal-vpn/xray-exporter:v0.4.0
docker run --rm \
  -p 9787:9787 \
  -v "$PWD/xray:/config/xray:ro" \
  -v "$PWD/xray-exporter-config.yml:/config/xray-exporter-config.yml:ro" \
  ghcr.io/opal-vpn/xray-exporter:v0.4.0
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
| `CONTROL_TIMEOUT` | `5s` | per direct [reachability](#reachability) probe |
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
mutually exclusive, `refresh` is remnawave-only, and an unknown key in the
block rejects the file. The block replaces nothing else: `targets:`,
`dns_resolvers:` and `dns_probe_domain` keep their meaning for both sources.

### The fetch is direct

The panel is contacted straight from the probe host: no environment proxy
(`HTTP_PROXY`/`HTTPS_PROXY` are ignored), no xray tunnel, no socks or DNS
inbound, the system resolver, TLS verified, and redirects only within the same
scheme and host. It is the one request outside a tunnel besides loopback (see
[No direct traffic](#no-direct-traffic)). A host that reaches the panel only
through a tunnel cannot fetch and shows the fail-closed state below; there is
no proxy or tunnel fallback.

One request per interval — none when `refresh: 0` — with
`Accept: application/json`, a 20 s timeout and an 8 MiB body cap. No token and
no cookie are sent.

### When a fetch fails

A non-2xx status, a timeout, invalid JSON or a body that is not a JSON array is
a fetch failure, and the remnawave source fails closed:

- every config from the last successful fetch stops and
  `xray_config_running` reads 0 for all of them
- their probes and resolvers reset to 0, their latency series disappear and
  their timestamps move to the failure time
- no measurement cycle runs
- `xray_exporter_config_fetch_errors_total` moves, while
  `xray_exporter_config_fetch_timestamp_seconds` keeps the time of the last
  success

The last good bytes are kept only to name those configs; they are never started
from that cache. Recovery is a successful fetch, which applies its set normally.
A failed first fetch is not fatal: the exporter starts with no configs, retries
every `refresh`, and with `refresh: 0` waits for `SIGHUP`.

A successful fetch of an empty array is not a failure: every config stops, the
fetch timestamp advances and `xray_exporter_config_fetch_errors_total` stays
flat. The fresh timestamp and the missing config series are the whole signal.

### A changed set is normal

A changed set is applied, counted in `xray_exporter_config_reload_total` and
measured: new instances start, removed ones stop and lose their series,
unchanged ones keep running. An unchanged set is a no-op while every config
runs, and re-applied if an expected instance is missing — a start that failed,
or a fail-closed outage.

Config content is not validated: xray decides whether a config runs. A fetched
config that cannot start is `xray_config_running` 0 with one WARN line and no
`reload_errors_total` movement, and the next successful fetch retries it. The
rest of the set keeps measuring; alert on `xray_config_running == 0` for this,
not on the reload error counter.

Labels come from the panel: a sanitised `remarks`, else the first outbound's
`address-port`, else `config-N` in array order; duplicates get `-2`, `-3`, … and
a label never fails the fetch. Renaming a host is a new series and the old
series is dropped.

### The directory source is unchanged

It is read at startup and on `SIGHUP`, never on a timer. A startup with an
empty or unreadable directory is fatal. On `SIGHUP` a read error keeps the
previous set, counts a reload error and increments
`xray_exporter_config_fetch_errors_total`, while a readable empty directory is
authoritative and stops every config. Its reload errors keep their old meaning:
a config that fails to start makes the reload an error.

### The file is a credential

The subscription URL embeds the probe user's shortUuid, so
`xray-exporter-config.yml` is a credential: whoever holds it can read the whole
subscription. Keep it out of images, logs and issues, mount it read-only, and
treat it as sensitive as the config files it replaces. The exporter never logs
the URL — errors name only the panel host — and never the response body.

### Migrating from `targets.yaml`

`xray-exporter-config.yml` replaces `targets.yaml`, and `EXPORTER_CONFIG_FILE`
replaces `TARGETS_FILE`. No alias is kept: a deployment still using the old
name reads nothing, so rename the file and its mount and switch the variable.
The YAML keys are unchanged, and `xray_config_source` is optional and additive,
so the migration itself changes no measurement.

The `opal-vpn` follow-up (a separate repository) renames
`configs/xray-exporter/targets.yaml` and its compose mount, switches the env
var, templates `xray_config_source` with the probe user's shortUuid, and stops
running the Python fetch that copies `.xray-exporter-configs`. Nothing in this
repository depends on that change.

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
| `xray_control_up` | kind, name, class | direct probe succeeded |
| `xray_control_seconds` | kind, name, class | last successful round trip |
| `xray_entry_tcp_up` | config | direct connect to the config's entry succeeded |
| `xray_entry_tcp_seconds` | config | last successful connect time |

Every expected series exists from the first scrape with `up` at 0. Latency
series appear only after a success — a 0 would chart as infinitely fast and
poison averages. A failed probe sets `up` to 0, moves the timestamp forward and
keeps the last successful latency, so staleness is visible through the
timestamp. Series disappear only with their config or target.

### Source health

Four queries cover the fetch. The three staleness queries are gated on
`source_info{type="remnawave"}`: directory mode stamps the fetch timestamp only
at startup and on `SIGHUP`, so its age says nothing about health.

```promql
# stale fetch: no success for longer than three default 5m refreshes
time() - max(xray_exporter_config_fetch_timestamp_seconds) > 900
  and on() (count(xray_exporter_config_source_info{type="remnawave"}) > 0)

# no first success: the timestamp is absent, so nothing was ever fetched
absent(xray_exporter_config_fetch_timestamp_seconds)
  and on() (count(xray_exporter_config_source_info{type="remnawave"}) > 0)

# fetch error rate: share of attempts that returned no usable set
rate(xray_exporter_config_fetch_errors_total[15m])
  / rate(xray_exporter_config_fetch_total[15m])

# stalled loop: no attempt at all in the last three refresh intervals
increase(xray_exporter_config_fetch_total[15m]) == 0
  and on() (count(xray_exporter_config_source_info{type="remnawave"}) > 0)
```

These windows assume the default `refresh: 5m`; scale `900` and `15m` to three
refreshes, and with `refresh: 0` drop the stale-fetch and stalled-loop queries,
which cannot distinguish a configured pause from a stall.

A fetch outage also blanks the fleet: every known config sits at
`xray_config_running` 0 while the timestamp ages. An empty subscription is the
opposite pair — a fresh timestamp and no config series at all.

## Reachability

Blocking differs by region, access type and operator, so a failing tunnel means
different things on a home line, on a SIM and in a datacenter. The optional
`reachability` block records which network the host is on and adds probes that
run **outside any tunnel**, so a failing tunnel can be told apart from a failing
network. Without the block the exporter sends none of this traffic; an empty
block (`reachability: {}`) turns on the network lookup alone.

```yaml
reachability:
  controls:                      # direct probes, once per INTERVAL
    https:
      - { name: ya,     host: ya.ru,      class: domestic }
      - { name: google, host: google.com, class: foreign }
    tcp:
      - { name: cf, addr: "1.1.1.1:443", class: foreign }
    dns:
      - { name: google, host: google.com, class: foreign }
  entry_tcp: true                # connect to each config's first outbound address
```

**The network** is looked up once an hour (and on `SIGHUP`) at `ipinfo.io` and
exported as `xray_network_info{as_name, country}`. Nothing is declared by hand.
It is a third party's best guess, shown and never acted on; a failed lookup
keeps the last good answer and logs a warning. Tag any probe with it:

```promql
xray_probe_up * on(instance) group_left(as_name, country) xray_network_info
```

**Controls** answer "what is this network doing": `https` is a GET through the
host's own resolver (up means any HTTP response), `tcp` a connect to a fixed
address, `dns` an `A` query through the system resolver. Each has a `class`,
`domestic` or `foreign`, and the split between them is the signal: domestic up
and foreign down looks like a whitelist regime, everything down is a dead line,
DNS down with TCP up is a DNS-level block. The exporter publishes raw results;
verdicts belong in Prometheus rules.

**`entry_tcp`** connects to the first outbound address of each running config.
Together with the tunnel probe it separates a blocked address or port (connect
fails) from a blocked protocol (connect succeeds, the tunnel does not). The
address is never a label or a log field.

## No direct traffic

The exporter sends no traffic outside a tunnel, with two exceptions: the config
source fetch itself (see [Config sources](#config-sources)) and, only when the
config has a `reachability` block, the [Reachability](#reachability) lookups and controls, which
describe the host's own network and so cannot go through a tunnel. HTTP and TLS
probes, DoH, DoT and the UDP DNS probe all reach their peers through an xray
instance's loopback inbound, so a config's `routing` decides the path. Apart
from those two exceptions, only loopback is dialed directly: the SOCKS port and DNS inbound the exporter starts,
and the free-port probe.

A tunnel that is down therefore surfaces as failed probes and
`xray_config_running` 0, not as measurements from a direct path.

## Logs

The exporter logs `slog` `key=value` lines to stdout: failed probes, one cycle
summary, instance start/failure/reload, and the outcome of each config fetch.
Successful probes and unchanged fetches log at debug; `LOG_LEVEL` (default
`info`) sets the threshold. A config that cannot start is logged at ERROR for
a directory source and at WARN for a fetched one, where it is a test result
rather than an exporter error.

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
