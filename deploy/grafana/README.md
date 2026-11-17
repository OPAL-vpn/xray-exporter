# Grafana dashboard

`dashboard.json` is importable as-is. Every panel reads its Prometheus through
the `datasource` template variable, so no UID is baked in and no `__inputs`
prompt is needed.

## Variables

`instance` → `config` → `target`, all multi-select with `All` as the default and
`.*` as the all-value, so the `=~` filters match every series when nothing is
narrowed down. `instance` is the Prometheus `instance` label from
`deploy/prometheus.yml`, not something the exporter sets.

`udp_target` is a second target list, taken from `xray_udp_up`: the UDP quality
row uses it so UDP target names never appear in the HTTPS `target` list. `node`
is single-select and drives the two Reachability detail timelines.

| Variable | Source |
|---|---|
| `datasource` | any Prometheus datasource |
| `instance` | `label_values(xray_config_running, instance)` |
| `config` | `label_values(xray_config_running{instance=~"$instance"}, config)` |
| `target` | `label_values(xray_probe_up{instance=~"$instance",config=~"$config"}, target)` |
| `udp_target` | `label_values(xray_udp_up{instance=~"$instance",config=~"$config"}, target)` |
| `node` | `label_values(xray_control_up, instance)` |

## Import via the UI

1. Grafana → Dashboards → New → Import.
2. Upload `dashboard.json` (or paste it into the JSON box).
3. Pick a folder and the Prometheus datasource when prompted; the dashboard's
   own `datasource` variable can switch it afterwards.

## Provisioning

Copy the dashboard where the file provider looks, then drop the provider snippet
next to the datasource snippet:

```sh
sudo install -d -m 0755 /var/lib/grafana/dashboards /etc/grafana/provisioning/dashboards /etc/grafana/provisioning/datasources
sudo install -m 0644 dashboard.json /var/lib/grafana/dashboards/dashboard.json
sudo install -m 0644 provisioning/dashboards.yml /etc/grafana/provisioning/dashboards/xray-exporter.yml
sudo install -m 0644 provisioning/datasources.yml /etc/grafana/provisioning/datasources/xray-exporter.yml
sudo systemctl restart grafana-server     # or docker compose restart grafana
```

`provisioning/dashboards.yml` puts the dashboard in the `VPN` folder with
`allowUiUpdates: false`: edits made in the UI are discarded on the next
provisioning pass, so change the JSON in this repo instead.
`provisioning/datasources.yml` points at `http://prometheus:9090` — the
compose-network name in `deploy/prometheus.yml`'s setup; change the URL if
Prometheus lives elsewhere.

## Panels

| Row | Panels |
|---|---|
| Overview | configs up, cycle age, config fetch age, 15m probe success ratio, 15m tunnel DNS up ratio |
| Performance | TTFB, DNS lookup and total request averages per node, DNS share of TTFB, body transfer |
| Reachability | egress network table, direct controls share up, entry TCP share reachable, per-node detail timelines for controls and entry TCP |
| Targets | target success table, failing targets table and timeline, HTTP status timeline, TTFB, total request time, tunnel DNS lookup |
| Resolvers | resolver success table, failing resolvers table, resolver latency |
| Config | config health table |
| UDP quality | UDP and TCP targets up, UDP loss ratio, UDP RTT, UDP RTT max, UDP jitter, TCP connect time |

Latency series are absent until the first successful measurement, so those
panels render empty rather than zero: `spanNulls` is off and every panel has a
`noValue` placeholder. Nothing in the dashboard requires a built-in target or
resolver list.

The UDP quality latency panels are in milliseconds: their queries scale the
seconds-valued gauges by 1000, the unit `--once` reports quality probes in.
