# Prometheus Qubes proxy

These programs allow you to monitor networks of Qubes OS VMs using Prometheus.

## Crash course on Prometheus

Prometheus is a pull-oriented system for monitoring any sort of devices that export information.  At the core, it's composed of several parts:

1. The Prometheus service proper.  This is a program that periodically scrapes a configured set of HTTP endpoints, and aggregates the results of the scrapes into a time series database.
2. Exporters.  These programs run on the various devices being monitored.  They provide an HTTP endpoint (customarily, `/metrics`) for Prometheus to scrape.  When a scrape takes place, the exporter serializes its current metrics and sends them via HTTP.

## How Prometheus fits in the Qubes OS world

It's tricky to get a pull-oriented system to harvest information from a system which has effectively no inbound networking.  Since Prometheus can't connect to your VMs — Qubes OS' networking model guarantees that — Prometheus can't collect or aggregate runtime statistics from any exporters you may be running on your VMs.

Here's where this program bridges the gap:

1. You install an exporter on your VMs / VM templates, then configure the exporter to start up on boot.  Exporters are usually lightweight, so they won't demand a lot of memory from your system's VMs.
2. You install the `ruddo.PrometheusProxy` Qubes RPC service in the VMs / VM templates where you're running the exporters you desire to scrape / expose.
3. You install the `prometheus-qubes-proxy` program (presumably) in your NetVM, then configure it to start on boot (you must enable `qvm-service` `prometheus-qubes-proxy` and restart the qube for the service to start).
4. You configure the appropriate Qubes RPC policy in `dom0` to allow `prometheus-qubes-proxy` in your NetVM to talk to other VMs' `ruddo.PrometheusProxy` service.
5. You configure your Prometheus instance to scrape the `/forward` endpoint of the NetVM `prometheus-qubes-proxy` (by default running on port 8199).  A sample snippet of the main Prometheus configuration file, using the proxy to scrape Node Exporter on non-networked VMs, follows:

```yaml
- job_name: node_exporter
  metrics_path: /metrics
  scheme: http
  follow_redirects: true
  enable_http2: true
  static_configs:
  - targets:
    - work:9100
    - personal:9100
    - files:9100
  relabel_configs:
  - # Tell Prometheus to scrape /forward instead of /metrics.
    source_labels: [__address__]
    regex: (.+)
    target_label: __metrics_path__
    replacement: /forward
    action: replace
  - # Tell Prometheus to put the name of the VM in query string parameter ?target=
    source_labels: [__address__]
    regex: (.+):(.+)
    target_label: __param_target
    replacement: ${1}
    action: replace
  - # Tell Prometheus to put the node exporter port (9100) in query string parameter ?port=
    source_labels: [__address__]
    regex: (.+):(.+)
    target_label: __param_port
    replacement: ${2}
    action: replace
  - # Finally, tell Prometheus to put the IP address (and port 8199)
    # (in our example, the NetVM running prometheus-qubes-proxy) as the
    # real address to talk to.
    source_labels: [__address__]
    regex: (.+)
    target_label: __address__
    replacement: 192.168.1.2:8199
    action: replace
```

All said and done, this is what happens:

* Prometheus contacts `prometheus-qubes-proxy` in the NetVM, asking to scrape the exporter running on VM `X`.
* `prometheus-qubes-proxy` opens a Qubes RPC connection to VM `X`, requesting the `ruddo.PrometheusProxy` service.
* `dom0` (the AdminVM) authorizes this request based on the policy configured in `/etc/qubes/policy.d` (the default `90-prometheus-proxy.policy` rejects everything by default, so you must explicitly configure an `allow` policy).
* `ruddo.PrometheusProxy` receives the Qubes RPC request.
* `prometheus-qubes-proxy` requests, via this RPC channel, to scrape the exporter running on `X`.
* `ruddo.PrometheusProxy` contacts the exporter running on `localhost` in `X`, then relays the reply back to `prometheus-qubes-proxy`
* `prometheus-qubes-proxy` relays the reply back to Prometheus.
* Prometheus stores the resulting metrics in its time series database.

That's all.

Any exporter exposing a `/metrics` endpoint can be serviced by this proxy.  The proxy will not query any other URL paths, for security reasons.

### Service discovery

Prometheus also supports service discovery, and in this program, service discovery is also supported (through URL path `/discover`).  The discovery reply will consist of all the running qubes (requested from `dom0` by the proxy).

This is also disallowed by default, but you can allow it by setting policy `ruddo.PrometheusDiscover` targeting the `dom0` VM from your NetVM to `allow`.

The benefit of using discovery (plus relabeling rules, as shown above) is that Prometheus will not waste your CPU time requesting metrics for qubes that aren't running.  This particular situation I have measured at about 40% usage of one whole CPU core in `dom0`.  Additionally, you will not get error messages in the `dom0` system log noting that a VM which is stopped will not be autostarted.

Note that this allows anyone with access to the NetVM's `prometheus-qubes-proxy` to query all names of running VMs.

Here is a sample response from the `/discover` endpoint:

```json
[
    {
        "targets": ["work", "personal", "vault"]
    }
]
```

## How to deploy this

These instructions assume you have already successfully deployed and run, in your Qubes OS VMs, a Prometheus exporter or more, such as the Prometheus node exporter.

### Source deploy of the software

If you'd rather deploy via RPMs, see the next subheading.

* copy the source to your VM template that your NetVM is based on
* install Go in the VM template
* run `make install` in the source directory
* `systemctl enable prometheus-qubes-proxy` after installation
* create an `/etc/qubes-rpc/policy/ruddo.PrometheusProxy` policy file in your `dom0` AdminVM
* add and enable the Qubes service `prometheus-qubes-proxy` to your NetVM
* stop the VM template, stop your NetVM, start the NetVM

You should see the proxy running in a process list.  Adjust the firewall rules of your NetVM to open the port the proxy is listening on (8199 by default), and you're ready to start scraping,

### Deploy of the software with RPMs

Build the RPM packages in a build VM with the command `make rpm`.

Install the `prometheus-qubes-proxy` package on your VM template.

Install the `prometheus-qubes-proxy-service` package on your VM template, or standalone, or even dom0.  The files of that package must be present on the VM whose metrics exporter you want to query via the proxy.

Install the `prometheus-qubes-proxy-dom0` package on your `dom0` AdminVM.

Enable the systemd service `prometheus-qubes-proxy.service` on your VM template.

Add the service `prometheus-qubes-proxy` to your NetVM (or, if you are using Qubes network server, to a VM exposed to the network).

Change the `ruddo.PrometheusProxy` policy file in `dom0` to authorize the VM with `prometheus-qubes-proxy` to contact the VMs you want to scrape.
