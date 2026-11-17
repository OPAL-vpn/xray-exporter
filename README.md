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
- **UDP quality through the tunnel**, with `udp_targets`: a burst of STUN
  binding requests to each target — loss, median and maximum RTT, jitter — and,
  with `udp_under_load`, the same burst while a download saturates the tunnel.
- **TCP connects through the tunnel**, with `tcp_targets`: a plain CONNECT to
  each host:port, for apps that are neither HTTP nor DNS.
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
| `PROBE_TIMEOUT` | `10s` | per HTTP/DNS/UDP/TCP probe; a UDP target's `timeout` overrides it |
| `RESOLVER_TIMEOUT` | `5s` | per DoH/DoT probe |
| `CONTROL_TIMEOUT` | `5s` | per direct reachability probe, controls and entry connects |
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
Fetches ask for `application/json`, time out after 20 s, cap the body at 8 MiB
and send no token or cookie.

### The directory source is unchanged

It is read at startup and on `SIGHUP`, never on a timer. A startup with an
empty or unreadable directory is fatal. On `SIGHUP` a read error keeps the
previous set, counts a reload error and increments
`xray_exporter_config_fetch_errors_total`, while a readable empty directory is
authoritative and stops every config. Its reload errors keep their old meaning:
a config that fails to start makes the reload an error.

## UDP quality targets

Three optional blocks measure UDP and plain TCP through each tunnel. Absent,
nothing is measured, like the reachability block.

```yaml
udp_targets:
  - name: tg-stun-1
    kind: stun
    addr: "203.0.113.10:3478"   # a Telegram call endpoint, or any STUN server
    count: 20                   # default
    spacing: 100ms              # default
    timeout: 15s                # default: PROBE_TIMEOUT (10s)
tcp_targets:
  - name: mtproto-dc4
    addr: "203.0.113.20:443"
udp_under_load:                 # adds the loaded pass
  url: https://mirror.example.com/20mb.bin
```

A UDP target is a burst of `count` STUN binding requests `spacing` apart,
through the config's own socks inbound (SOCKS5 UDP ASSOCIATE, so XUDP for the
stream protocols) — the first packet pattern of a Telegram call. `kind` is
`stun`; any binding reply carrying the burst's transaction id counts, success
or error, so Telegram's TURN servers, which answer binding requests
unauthenticated, are valid targets. `tcp_targets` are plain CONNECTs through the
same tunnel, for non-HTTP TCP apps. `name` is the `target` label, so names must
be unique across both blocks.

`count` is 1–1000 (0 means the default 20) and `spacing` 1 ms–1 s. `count ×
spacing` must stay below the target's `timeout`: the timeout bounds the whole
burst, send plus collection, as every other probe's timeout does, and a burst
the cycle deadline cuts is discarded rather than published with a truncated
denominator. The whole cycle has to fit `INTERVAL/2`: every burst, the doubled
loaded pass when `udp_under_load` is set, and everything else. The defaults — a
2 s burst — leave wide margins at `INTERVAL 5m`, while a `PROBE_TIMEOUT`
shorter than a target's burst makes that target time out every cycle. Probes
are cycle-bound like the rest: one sample per config and target per `INTERVAL`,
so minutes-scale degradation shows as a run of degraded samples.

### Where the targets come from

Telegram's STUN/TURN endpoints are per-call and ephemeral, so the exporter never
fetches them: extract them once and pin them as targets.

1. **MTProto one-liner** — with any account (e.g. Telethon), call
   `phone.getCallConfig` (deprecated but live) and read `stun_servers` and
   `turn_servers[].uris`. TURN credentials are per-call and unusable here, but
   the hosts still answer STUN binding.
2. **Capture a real call** — tdesktop's debug log, or Wireshark on the client,
   shows the UDP endpoints the call used; they are stable enough across calls to
   pin.
3. **Public STUN fallback** — any public STUN server
   (`stun.cloudflare.com:3478`, `stun.l.google.com:19302`) measures the same
   axes against a third party.

Same servers, same packet type, same tunnel path as a real call, without
impersonating a subscriber. It is also the reliability model: a Telegram
endpoint that goes away is a fact real calls share, so it reads as signal
rather than as a broken probe. A target that persistently fails while other UDP
targets on the same config answer is a target problem; every target failing
while `xray_dns_up` is 1 is the tunnel.

### The under-load pass

`udp_under_load` probes every UDP target twice per cycle: the idle burst, then
the same burst while a bulk HTTPS GET of `url` runs through the same tunnel. The
GET starts before the burst, is cancelled when the burst completes and restarts
whenever it finishes early, so bytes flow for the whole burst; each loaded burst
owns its download. Traffic cost is burst duration × achievable rate per target
per cycle, not file size, so point `url` at a moderate file you are entitled to
fetch repeatedly. The loaded series is labelled `load="loaded"`; without the
block only `idle` series exist.

`url` must be absolute `http(s)` and must point at the file itself: the
exporter's HTTP client does not follow redirects, so a redirecting link fails as
a non-2xx. A loaded pass whose download moved zero bytes is discarded — the
previous loaded sample and its timestamp stay, so staleness stays visible — and
`xray_udp_load_up` goes to 0. That gauge is generator health, not tunnel health:
the idle series never depend on the URL. A target whose burst is too short for
the first GET to move bytes reports 0 for the same reason, so the burst must be
long enough for the download to start — the defaults (count 20, spacing 100 ms)
give a burst of about two seconds, which is long enough in practice.

### Reading the numbers

- `xray_dns_up` 1 with `xray_udp_up` 0 — the server egress passes port-53 UDP
  and drops the rest: calls and Factorio dead while browsing is fine.
- `xray_udp_loss_ratio` above ~1–2 % or `xray_udp_jitter_seconds` above ~30 ms —
  call quality audibly degrades and Factorio input delays; look at the server
  egress and the carrier path.
- `xray_udp_rtt_max_seconds` far above `xray_udp_rtt_seconds` with low loss —
  the XUDP stall signature, carrier TCP retransmit pauses: rubber-banding.
- Idle/loaded divergence — RTT or jitter degrading only at `load="loaded"` —
  congestion or QoS collapse under load, not a routing fault; compare across
  configs to find which tunnel holds up.
- The burst follows the config's own routing rules, so a config that steers UDP
  differently from HTTPS measures its real user path. That is the point, but it
  is a per-config property, not a measurement error.

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
| `xray_network_info` | as_name, country | 1, the network the host egresses from (ipinfo.io); reachability only |
| `xray_network_timestamp_seconds` | — | last successful egress lookup; absent until then |
| `xray_control_up` | kind, name, class | direct probe succeeded |
| `xray_control_seconds` | kind, name, class | last successful round trip |
| `xray_entry_tcp_up` | config | direct connect to the config's entry succeeded |
| `xray_entry_tcp_seconds` | config | last successful connect time |
| `xray_udp_up` | config, target, load | a matching reply arrived |
| `xray_udp_loss_ratio` | config, target, load | share of datagrams lost, 0..1 |
| `xray_udp_rtt_seconds` | config, target, load | median RTT |
| `xray_udp_rtt_max_seconds` | config, target, load | maximum RTT |
| `xray_udp_jitter_seconds` | config, target, load | mean absolute RTT change |
| `xray_udp_timestamp_seconds` | config, target, load | last completed burst |
| `xray_tcp_up` | config, target | tunnel CONNECT succeeded |
| `xray_tcp_seconds` | config, target | last successful connect time |
| `xray_udp_load_up` | config | the under-load download moved bytes; absent until the first loaded pass |

Every expected series exists from the first scrape with `up` at 0. Latency
series appear only after a success — a 0 would chart as infinitely fast and
poison averages. A failed probe sets `up` to 0, moves the timestamp forward and
keeps the last successful latency, so staleness is visible through the
timestamp. Series disappear only with their config or target. The one exception
is `xray_udp_load_up`: it appears with the first loaded pass and disappears
again when `udp_under_load` is removed.

## Dashboard

`deploy/grafana/dashboard.json` imports as-is into any Grafana with a Prometheus
datasource: every panel reads a `datasource` variable, so no UID is baked in. It
ships `instance` → `config` → `target` variables, all multi-select with `All` as
the default, plus a `udp_target` variable for the UDP quality row — UDP target
names would otherwise pollute the HTTPS target list. See
`deploy/grafana/README.md`.

## Licence

xray-exporter is licensed under the MIT Licence — see `LICENSE`.

xray-core is licensed under MPL-2.0 and is linked into this binary as a Go
dependency; the image contains no separate xray executable. The injection
approach is derived from `github.com/batonogov/xray-health-exporter` (MIT) —
see `NOTICE`.
