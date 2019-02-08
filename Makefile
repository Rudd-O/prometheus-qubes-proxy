BINDIR=/usr/bin
SYSCONFDIR=/etc
UNITDIR=/usr/lib/systemd/system
DESTDIR=
PROGNAME=prometheus-qubes-proxy

prometheus-qubes-proxy: *.go
	GOPATH=$$PWD go build
	if ! test -f prometheus-qubes-proxy ; then for f in prometheus-qubes-proxy-*.*.* ; do if test -f "$$f" ; then mv "$$f" prometheus-qubes-proxy ; fi ; done ; fi

clean:
	rm -f prometheus-qubes-proxy
	rm -f *.tar.gz *.rpm

dist: clean
	excludefrom= ; test -f .gitignore && excludefrom=--exclude-from=.gitignore ; DIR=$(PROGNAME)-`awk '/^Version:/ {print $$2}' $(PROGNAME).spec` && FILENAME=$$DIR.tar.gz && tar cvzf "$$FILENAME" --exclude="$$FILENAME" --exclude=.git --exclude=.gitignore $$excludefrom --transform="s|^|$$DIR/|" --show-transformed *

rpm: dist
	@which rpmbuild || { echo 'rpmbuild is not available.  Please install the rpm-build package with the command `dnf install rpm-build` to continue, then rerun this step.' ; exit 1 ; }
	T=`mktemp -d` && rpmbuild --define "_topdir $$T" -ta $(PROGNAME)-`awk '/^Version:/ {print $$2}' $(PROGNAME).spec`.tar.gz || { rm -rf "$$T"; exit 1; } && mv "$$T"/RPMS/*/* "$$T"/SRPMS/* . || { rm -rf "$$T"; exit 1; } && rm -rf "$$T"

srpm: dist
	T=`mktemp -d` && rpmbuild --define "_topdir $$T" -ts $(PROGNAME)-`awk '/^Version:/ {print $$2}' $(PROGNAME).spec`.tar.gz || { rm -rf "$$T"; exit 1; } && mv "$$T"/SRPMS/* . || { rm -rf "$$T"; exit 1; } && rm -rf "$$T"

install-prometheus-qubes-proxy: prometheus-qubes-proxy
	install -Dm 755 prometheus-qubes-proxy -t $(DESTDIR)/$(BINDIR)/

install-prometheus-qubes-proxy.service: prometheus-qubes-proxy.service
	install -Dm 644 prometheus-qubes-proxy.service -t $(DESTDIR)/$(UNITDIR)/

install-ruddo.PrometheusProxy: ruddo.PrometheusProxy
	install -Dm 644 ruddo.PrometheusProxy -t $(DESTDIR)/$(SYSCONFDIR)/qubes-rpc/

install-ruddo.PrometheusProxy.policy: ruddo.PrometheusProxy.policy
	install -Dm 644 ruddo.PrometheusProxy.policy $(DESTDIR)/$(SYSCONFDIR)/qubes-rpc/policy/ruddo.PrometheusProxy

install: install-prometheus-qubes-proxy install-ruddo.PrometheusProxy install-ruddo.PrometheusProxy.policy install-prometheus-qubes-proxy.service

.PHONY = clean dist rpm srpm install
