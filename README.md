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
3. You install the `prometheus-qubes-proxy` program (presumably) in your NetVM, then configure it to start on boot.
4. You configure the appropriate Qubes RPC policy to allow `prometheus-qubes-proxy` in your NetVM to talk to other VMs' `ruddo.PrometheusProxy` service.

All said and done, this is what happens:

* Prometheus contacts `prometheus-qubes-proxy` in the NetVM, asking to scrape the exporter running on VM `X`.
* `prometheus-qubes-proxy` opens a Qubes RPC connection to VM `X`, requesting the `ruddo.PrometheusProxy` service.
* `dom0` (the AdminVM) authorizes this request based on the policy configured in `/etc/qubes/policy.d` (the default `90-prometheus-proxy.policy` rejects everything by default, so you must configure this by hand).
* `ruddo.PrometheusProxy` receives the Qubes RPC request.
* `prometheus-qubes-proxy` requests, via this RPC channel, to scrape the exporter running on `X`.
* `ruddo.PrometheusProxy` contacts the exporter running on `localhost` in `X`, then relays the reply back to `prometheus-qubes-proxy`
* `prometheus-qubes-proxy` relays the reply back to Prometheus.
* Prometheus stores the resulting metrics in its time series database.

That's all.

In principle, any exporter exposing a `/metrics` endpoint can be serviced by this proxy.

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
