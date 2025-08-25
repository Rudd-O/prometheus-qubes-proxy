package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errRequestRefused = errors.New("request refused")
var errShortWrite = errors.New("short write")
var errShortRead = errors.New("short read")

type qrexecConnMultiplexer struct {
	vmPortMap map[string]*qrexecConn
	m         sync.Mutex
}

func newQrexecConnMultiplexer() *qrexecConnMultiplexer {
	return &qrexecConnMultiplexer{
		vmPortMap: make(map[string]*qrexecConn),
	}
}

type qrexecConnLogger struct {
	vm                string
	serviceIdentifier string
	logged            map[string]bool
	m                 sync.Mutex
}

func newqrexecConnLogger(vm string, serviceIdentifier string) *qrexecConnLogger {
	l := qrexecConnLogger{
		vm:                vm,
		serviceIdentifier: serviceIdentifier,
		logged:            make(map[string]bool),
	}
	return &l
}

func (l *qrexecConnLogger) Error(formatString string, args ...interface{}) {
	l.m.Lock()
	defer func() {
		l.m.Unlock()
	}()
	key := fmt.Sprintf("%s:%s", l.vm, l.serviceIdentifier)
	if !l.logged[key] {
		x := fmt.Sprintf(formatString, args...)
		log.Printf("%s:%s: %s", l.vm, l.serviceIdentifier, x)
		l.logged[key] = true
	}
}

func (l *qrexecConnLogger) OK(formatString string, args ...interface{}) {
	l.m.Lock()
	defer func() {
		l.m.Unlock()
	}()
	if formatString != "" {
		x := fmt.Sprintf(formatString, args...)
		log.Printf("%s:%s: %s", l.vm, l.serviceIdentifier, x)
		log.Printf("%s:%s: %s", l.vm, l.serviceIdentifier, "(further messages will be suppressed until success)")
	}
	key := fmt.Sprintf("%s:%s", l.vm, l.serviceIdentifier)
	l.logged[key] = false
}

func (q *qrexecConnMultiplexer) query(vm string, service string, arg string) (io.Reader, error) {
	lookup := func(vm string, service string, arg string) *qrexecConn {
		q.m.Lock()
		defer func() {
			q.m.Unlock()
		}()
		identifier := fmt.Sprintf("%s-%s-%s", service, vm, arg)
		conn, ok := q.vmPortMap[identifier]
		if ok {
			return conn
		}
		q.vmPortMap[identifier] = &qrexecConn{
			targetVM:      vm,
			targetService: service,
			targetArg:     arg,
			logger:        newqrexecConnLogger(vm, identifier),
		}
		return q.vmPortMap[identifier]
	}
	conn := lookup(vm, service, arg)
	return conn.query()
}

type qrexecConn struct {
	targetVM      string
	targetService string
	targetArg     string
	cmd           *exec.Cmd
	cancel        context.CancelFunc
	cancelCommand context.CancelFunc
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	m             sync.Mutex
	logger        *qrexecConnLogger
}

func (q *qrexecConn) query() (io.Reader, error) {
	q.m.Lock()
	defer func() {
		q.m.Unlock()
	}()

	if q.cmd == nil {
		q.cancel = func() {
			if q.stdout != nil {
				q.stdout.Close()
				q.stdout = nil
			}
			if q.stdin != nil {
				q.stdin.Close()
				q.stdin = nil
			}
			if q.cmd != nil {
				q.cancelCommand()
				q.cmd.Wait()
				q.cmd = nil
				q.cancelCommand = nil
			}
			if q.cancelCommand != nil {
				q.cancelCommand()
				q.cancelCommand = nil
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		q.cancelCommand = cancel

		arg := q.targetService
		if q.targetArg != "" {
			arg = fmt.Sprintf("%s+%s", q.targetService, q.targetArg)
		}
		c := exec.CommandContext(ctx, "qrexec-client-vm", q.targetVM, arg)
		q.cmd = c

		var err error
		q.stdin, err = c.StdinPipe()
		if err != nil {
			q.logger.Error("failed to launch program stdin pipe: %s.", err)
			q.cancel()
			return nil, err
		}

		q.stdout, err = c.StdoutPipe()
		if err != nil {
			q.logger.Error("failed to launch program stdout pipe: %s.", err)
			q.cancel()
			return nil, err
		}
		var stderr strings.Builder
		c.Stderr = &stderr

		err = c.Start()
		if err != nil {
			q.logger.Error("failed to start qrexec-client-vm: %s.", err)
			q.cancel()
			return nil, err
		}

		cleanedUpStderr := func() string {
			return strings.ReplaceAll(strings.TrimSuffix(stderr.String(), "\n"), "\n", "\\n")
		}

		wrt, err := q.stdin.Write([]byte("+\n"))
		if err != nil {
			result := c.Wait()
			q.logger.Error("failed to write initial handshake: %s (stderr: %s, result: %s).", err, cleanedUpStderr(), result)
			q.cancel()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if wrt != 2 {
			result := c.Wait()
			q.logger.Error("failed to write initial handshake (short write of %d bytes, stderr: %s, result: %s).", wrt, cleanedUpStderr(), result)
			q.cancel()
			return nil, errRequestRefused
		}

		var mybuf [2]byte
		n, err := q.stdout.Read(mybuf[:])
		if err != nil {
			result := c.Wait()
			q.logger.Error("failed to read initial handshake: %s (stderr: %s, result: %s).", err, cleanedUpStderr(), result)
			q.cancel()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if n != 2 || string(mybuf[:]) != "=\n" {
			q.logger.Error("failed to read initial handshake (wrong contents).")
			q.cancel()
			return nil, errRequestRefused
		}
	}

	n, err := q.stdin.Write([]byte("?\n"))
	if err != nil {
		q.logger.Error("failed to write handshake: %s.", err)
		q.cancel()
		return nil, err
	}
	if n != 2 {
		q.cancel()
		q.logger.Error("failed to write handshake (short write).")
		return nil, errShortWrite
	}

	readSoFar := []byte{}
	readLength := false
	for i := 0; i < 8; i++ { // max 9999999 bytes following read
		var buf [1]byte
		n, err := io.ReadFull(q.stdout, buf[:])
		if i == 0 && n == 0 {
			q.logger.Error("failed to read length byte (nothing read).")
			q.cancel()
			return nil, errShortRead
		}
		if err != nil {
			q.logger.Error("failed to read length byte: %s.", err)
			q.cancel()
			return nil, err
		}
		if buf[0] == '\n' {
			readLength = true
			break
		}
		readSoFar = append(readSoFar, buf[0])
	}
	if !readLength {
		q.logger.Error("data returned from exporter is too big")
		q.cancel()
		return nil, fmt.Errorf("number too big")
	}

	length, err := strconv.Atoi(string(readSoFar))
	if err != nil || length < 1 {
		q.logger.Error("data returned from exporter is too small: %s", err)
		q.cancel()
		return nil, fmt.Errorf("number too small")
	}

	b := make([]byte, length)
	_, err = io.ReadFull(q.stdout, b)
	if err != nil {
		q.logger.Error("failed to read payload from exporter: %s", err)
		q.cancel()
		return nil, err
	}

	q.logger.OK("")
	return bytes.NewBuffer(b), err
}

type myHandler struct {
	port int
	conn *qrexecConnMultiplexer
}

func (m *myHandler) serveDiscovery(w http.ResponseWriter) {
	// log.Printf("Incoming metrics request for exporter on port %v in VM %v", port, vm)
	vm := "dom0"
	reader, err := m.conn.query(vm, "ruddo.PrometheusDiscover", "")
	if err != nil {
		// log.Printf("Metrics query for exporter on port %v in VM %v failed: %s", port, vm, err)
		if err == errRequestRefused {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(500)
		}
		fmt.Fprintf(w, "%s\n", err)
		return
	}
	queriedData, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("%v: discovery data read from exporter failed: %s", vm, err)
		w.WriteHeader(500)
		return
	}

	vmsSplit := strings.Split(string(queriedData[:]), "\n")
	var vms []string
	for _, s := range vmsSplit {
		if strings.TrimSpace(s) != "" {
			vms = append(vms, s)
		}
	}
	res := []map[string][]string{{"targets": vms}}

	marshaled, err := json.Marshal(res)
	if err != nil {
		log.Printf("%v: discovery marshal to JSON failed: %s", vm, err)
	}

	w.Header().Add("Content-Type", "application/json")
	w.Header().Add("Content-Length", fmt.Sprintf("%d", len(marshaled)))
	w.Header().Add("Connection", "close")
	w.WriteHeader(200)

	_, err = w.Write(marshaled)
	if err != nil {
		log.Printf("%v: discovery write to scraper failed: %s", vm, err)
	}
}

func (m *myHandler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	uPort := r.URL.Query().Get("port")
	uVM := r.URL.Query().Get("target")

	port, err := strconv.Atoi(uPort)
	if port < 1025 || port > 65535 || err != nil {
		w.WriteHeader(400)
		fmt.Fprintf(w, "port is a mandatory query string parameter and it cannot be outside the 1025-65535 range\n")
		return
	}

	uVMa := strings.Split(uVM, ".")
	if len(uVMa) == 0 {
		uVMa = []string{" "}
	}
	uVM = uVMa[0]
	VMre := regexp.MustCompile("[a-zA-Z0-9_-]{1,32}")
	if !VMre.MatchString(uVM) {
		w.WriteHeader(400)
		fmt.Fprintf(w, "target is a mandatory query string parameter and it must conform to the standards of VM names\n")
		return
	}
	vm := uVM

	// log.Printf("Incoming metrics request for exporter on port %v in VM %v", port, vm)
	reader, err := m.conn.query(vm, "ruddo.PrometheusProxy", strconv.Itoa(port))
	if err != nil {
		// log.Printf("Metrics query for exporter on port %v in VM %v failed: %s", port, vm, err)
		if err == errRequestRefused {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(500)
		}
		fmt.Fprintf(w, "%s\n", err)
		return
	}
	queriedData, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("%v:%v: metrics read from exporter failed: %s", vm, port, err)
		w.WriteHeader(500)
		return
	}

	w.Header().Add("Content-Type", "text/plain; version=0.0.4")
	w.Header().Add("Content-Length", fmt.Sprintf("%d", len(queriedData)))
	w.Header().Add("Connection", "close")
	w.WriteHeader(200)

	_, err = w.Write(queriedData)
	if err != nil {
		log.Printf("%v:%v: metrics write to scraper failed: %s", vm, port, err)
		return
	}
}

func (m *myHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/discover" {
		m.serveDiscovery(w)
		return
	}

	if r.URL.Path == "/forward" {
		m.serveMetrics(w, r)
		return
	}

	w.WriteHeader(404)
	fmt.Fprintf(w, "the only valid URIs in this service are /forward and /discover\n")
}

var port = flag.Int("port", 8199, "Which port to listen on.")

func main() {
	if *port < 1025 || *port > 65535 {
		log.Fatalf("port cannot be outside the 1025-65535 range")
	}
	addr := fmt.Sprintf(":%d", *port)
	c := newQrexecConnMultiplexer()
	s := &http.Server{
		Addr:           addr,
		Handler:        &myHandler{*port, c},
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 10,
		IdleTimeout:    1,
	}
	log.Printf("Starting to serve on address %s", addr)
	log.Fatal(s.ListenAndServe())
}
