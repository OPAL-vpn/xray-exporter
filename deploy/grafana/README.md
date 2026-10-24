# Grafana dashboard

`dashboard.json` is importable as-is. Every panel reads its Prometheus through
the `datasource` template variable, so no UID is baked in and no `__inputs`
prompt is needed.

## Variables

`instance` → `config` → `target`, all multi-select with `All` as the default and
`.*` as the all-value, so the `=~` filters match every series when nothing is
narrowed down. `instance` is the Prometheus `instance` label from
`deploy/prometheus.yml`, not something the exporter sets.

| Variable | Source |
|---|---|
| `datasource` | any Prometheus datasource |
| `instance` | `label_values(xray_config_running, instance)` |
| `config` | `label_values(xray_config_running{instance=~"$instance"}, config)` |
| `target` | `label_values(xray_probe_up{instance=~"$instance",config=~"$config"}, target)` |

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
| Overview | configs up, cycle age, 15m probe success ratio |
| Targets | target status timeline, latest per target table, TTFB, total request time, tunnel DNS lookup, HTTP status timeline, TLS verified timeline |
| Resolvers | resolver availability timeline, resolver lookup latency |
| Config | config health table, reload errors |

Latency series are absent until the first successful measurement, so those
panels render empty rather than zero: `spanNulls` is off and every panel has a
`noValue` placeholder. Nothing in the dashboard requires a built-in target or
resolver list.

`internal/metrics/dashboard_test.go` parses this file, reads every `expr` and
every variable `query`/`definition`, and fails if it names a metric the exporter
does not register — rename a metric in `internal/metrics` and the drift test
tells you which panel to fix. It also fails if a panel expression ignores
`$instance`, so a panel cannot silently stay unscoped.
