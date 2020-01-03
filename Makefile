BINDIR=/usr/bin
SYSCONFDIR=/etc
UNITDIR=/usr/lib/systemd/system
DESTDIR=
PROGNAME=prometheus-qubes-proxy

ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

prometheus-qubes-proxy: *.go
	GOPATH=$$PWD go build
	if ! test -f prometheus-qubes-proxy ; then for f in prometheus-qubes-proxy-*.*.* ; do if test -f "$$f" ; then mv "$$f" prometheus-qubes-proxy ; fi ; done ; fi

.PHONY: clean dist rpm srpm install

clean:
	cd $(ROOT_DIR) && find -name '*~' -print0 | xargs -0r rm -fv && rm -fr *.tar.gz *.rpm && rm -f prometheus-qubes-proxy

dist: clean
	@which rpmspec || { echo 'rpmspec is not available.  Please install the rpm-build package with the command `dnf install rpm-build` to continue, then rerun this step.' ; exit 1 ; }
	cd $(ROOT_DIR) || exit $$? ; excludefrom= ; test -f .gitignore && excludefrom=--exclude-from=.gitignore ; DIR=`rpmspec -q --queryformat '%{name}-%{version}\n' *spec | head -1` && FILENAME="$$DIR.tar.gz" && tar cvzf "$$FILENAME" --exclude="$$FILENAME" --exclude=.git --exclude=.gitignore $$excludefrom --transform="s|^|$$DIR/|" --show-transformed *

srpm: dist
	@which rpmbuild || { echo 'rpmbuild is not available.  Please install the rpm-build package with the command `dnf install rpm-build` to continue, then rerun this step.' ; exit 1 ; }
	cd $(ROOT_DIR) || exit $$? ; rpmbuild --define "_srcrpmdir ." -ts `rpmspec -q --queryformat '%{name}-%{version}.tar.gz\n' *spec | head -1`

rpm: dist
	@which rpmbuild || { echo 'rpmbuild is not available.  Please install the rpm-build package with the command `dnf install rpm-build` to continue, then rerun this step.' ; exit 1 ; }
	cd $(ROOT_DIR) || exit $$? ; rpmbuild --define "_srcrpmdir ." --define "_rpmdir builddir.rpm" -ta `rpmspec -q --queryformat '%{name}-%{version}.tar.gz\n' *spec | head -1`
	cd $(ROOT_DIR) ; mv -f builddir.rpm/*/* . && rm -rf builddir.rpm

install-prometheus-qubes-proxy: prometheus-qubes-proxy
	install -Dm 755 prometheus-qubes-proxy -t $(DESTDIR)/$(BINDIR)/

install-prometheus-qubes-proxy.service: prometheus-qubes-proxy.service
	install -Dm 644 prometheus-qubes-proxy.service -t $(DESTDIR)/$(UNITDIR)/

install-ruddo.PrometheusProxy: ruddo.PrometheusProxy
	install -Dm 644 ruddo.PrometheusProxy -t $(DESTDIR)/$(SYSCONFDIR)/qubes-rpc/

install-ruddo.PrometheusProxy.policy: ruddo.PrometheusProxy.policy
	install -Dm 644 ruddo.PrometheusProxy.policy $(DESTDIR)/$(SYSCONFDIR)/qubes-rpc/policy/ruddo.PrometheusProxy

install: install-prometheus-qubes-proxy install-ruddo.PrometheusProxy install-ruddo.PrometheusProxy.policy install-prometheus-qubes-proxy.service
