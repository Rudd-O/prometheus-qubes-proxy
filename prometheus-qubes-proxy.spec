%define debug_package %{nil}

%define mybuildnumber %{?build_number}%{?!build_number:1}

Name:           prometheus-qubes-proxy
Version:        0.0.5.4
Release:        %{mybuildnumber}%{?dist}
Summary:        Proxy the outside world into Prometheus exporters running on your Qubes OS VMs

License:        GPLv3+
URL:            https://github.com/Rudd-O/prometheus-qubes-proxy
Source0:	https://github.com/Rudd-O/%{name}/archive/{%version}.tar.gz#/%{name}-%{version}.tar.gz

BuildRequires:  make
BuildRequires:  coreutils
BuildRequires:  tar
BuildRequires:  gawk
BuildRequires:  findutils
BuildRequires:  findutils
BuildRequires:  golang

Requires(pre): shadow-utils

BuildRequires:    systemd-rpm-macros

%description
This package lets your Prometheus server contact Prometheus exporters running
in your Qubes OS VMs.

Install this package on your VM / VM template in order to allow the outside world
to contact Prometheus Qubes proxy.  Then install the %{name}-service package on
the VM / VM templates where you'll run the exporter you wish to scrape.

%package service
Summary:        This component lets the Prometheus Qubes proxy talk to a VM.

%description service
This package installs the necessary component for Prometheus Qubes proxy to talk
to a Prometheus exporter run in a VM.

Install this package on your VM / VM template in order to allow Prometheus Qubes
proxy to talk to any exporters running on said VM / on any VMs derived from that
VM template.

Requires: curl
Requires: bash

%package dom0
Summary:        This component installs the default-deny proxy policy to dom0.

%description dom0
This package installs the default deny-all policy for ruddo.PrometheusProxy to
your dom0.

Install this package on your dom0 and then adjust the policy file to suit your
specific requirements.

%prep
%setup -q

%build
make DESTDIR=$RPM_BUILD_ROOT BINDIR=%{_bindir} SYSCONFDIR=%{_sysconfdir} UNITDIR=%{_unitdir}

%install
rm -rf $RPM_BUILD_ROOT
# variables must be kept in sync with build
make install DESTDIR=$RPM_BUILD_ROOT BINDIR=%{_bindir} SYSCONFDIR=%{_sysconfdir} UNITDIR=%{_unitdir}
echo "enable %{name}.service" > 70-%{name}.preset
install -Dm 644 70-%{name}.preset -t $RPM_BUILD_ROOT/%{_presetdir}/

%post
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun_with_restart %{name}.service

%files
%attr(0755, root, root) %{_bindir}/%{name}
%attr(0644, root, root) %{_unitdir}/%{name}.service
%attr(0644, root, root) %{_presetdir}/70-%{name}.preset
%doc README.md TODO

%files service
%attr(0755, root, root) %{_sysconfdir}/qubes-rpc/ruddo.PrometheusProxy

%files dom0
%attr(0644, root, root) %config(noreplace) %{_sysconfdir}/qubes-rpc/policy/ruddo.PrometheusProxy

%changelog
* Fri Feb 8 2019 Manuel Amador (Rudd-O) <rudd-o@rudd-o.com>
- Initial release
