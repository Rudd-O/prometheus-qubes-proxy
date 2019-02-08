package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errRequestRefused = errors.New("request refused")
var errShortWrite = errors.New("short write")

func SplitAt(substring string) func(data []byte, atEOF bool) (advance int, token []byte, err error) {

	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {

		// Return nothing if at end of file and no data passed
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		// Find the index of the input of the separator substring
		if i := strings.Index(string(data), substring); i >= 0 {
			return i + len(substring), data[0:i], nil
		}

		// If at end of file with data return the data74
		if atEOF {
			return len(data), data, nil
		}

		return
	}
}

type proxyReader struct {
	r io.Reader
	l int
}

func newProxyReader(r io.Reader) io.Reader {
	return &proxyReader{
		r: r,
		l: -1,
	}
}

func (r *proxyReader) Read(buf []byte) (int, error) {
	if r.l < 0 {
		read := []byte{}
		for {
			if len(read) > 8 {
				return 0, fmt.Errorf("number too big")
			}
			mybuf := []byte{0}
			n, err := r.r.Read(mybuf)
			if err != nil {
				return n, err
			}
			if n == 0 {
				return n, fmt.Errorf("short read")
			}
			if string(mybuf) == "\n" {
				// Done reading header.
				break
			}
			read = append(read, mybuf...)
		}
		var err error
		r.l, err = strconv.Atoi(string(read))
		if err != nil {
			r.l = -1
			return 0, err
		}
		if r.l < 0 {
			return 0, fmt.Errorf("number too small")
		}
	}
	m := r.l
	if m >= len(buf) {
		m = len(buf) - 1
	}
	n, err := r.r.Read(buf[:m])
	r.l = r.l - n
	if r.l == 0 || err == io.EOF {
		r.l = -1
		err = io.EOF
	}
	return n, err
}

type qrexecConnMultiplexer struct {
	vmPortMap map[string]*qrexecConn
	m         sync.Mutex
}

func newQrexecConnMultiplexer() *qrexecConnMultiplexer {
	return &qrexecConnMultiplexer{
		vmPortMap: make(map[string]*qrexecConn),
	}
}

func (q *qrexecConnMultiplexer) query(vm string, port int) (io.Reader, error) {
	lookup := func(vm string, port int) *qrexecConn {
		q.m.Lock()
		defer q.m.Unlock()
		identifier := fmt.Sprintf("%s-%d", vm, port)
		conn, ok := q.vmPortMap[identifier]
		if ok {
			return conn
		}
		q.vmPortMap[identifier] = &qrexecConn{
			targetVm:   vm,
			targetPort: port,
		}
		return q.vmPortMap[identifier]
	}
	conn := lookup(vm, port)
	return conn.query()
}

type qrexecConn struct {
	targetVm   string
	targetPort int
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	filter     io.Reader
	m          sync.Mutex
}

func (q *qrexecConn) query() (io.Reader, error) {
	q.m.Lock()
	defer q.m.Unlock()

	cancelResources := func() {
		q.cmd = nil
		q.cancel = nil
		q.stdin = nil
		q.stdout = nil
	}
	cancelProg := func() {
		q.cancel()
		q.stdin.Close()
		q.stdout.Close()
		cancelResources()
	}
	cancelFilter := func() {
		cancelProg()
		q.filter = nil
	}

	if q.cmd == nil {
		ctx, cancel := context.WithCancel(context.Background())
		q.cancel = cancel
		c := exec.CommandContext(ctx, "qrexec-client-vm", q.targetVm, fmt.Sprintf("ruddo.PrometheusProxy+%v", q.targetPort))
		q.cmd = c
		var err error
		q.stdin, err = c.StdinPipe()
		if err != nil {
			cancelResources()
			return nil, err
		}
		q.stdout, err = c.StdoutPipe()
		if err != nil {
			cancelResources()
			return nil, err
		}
		c.Stderr = os.Stderr
		err = c.Start()
		if err != nil {
			cancelResources()
			return nil, err
		}
		wrt, err := q.stdin.Write([]byte("+\n"))
		if err != nil {
			cancelProg()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if wrt != 2 {
			cancelProg()
			return nil, errRequestRefused
		}
		var mybuf [2]byte
		n, err := q.stdout.Read(mybuf[:])
		if err != nil {
			cancelProg()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if n != 2 || string(mybuf[:]) != "=\n" {
			cancelProg()
			return nil, errRequestRefused
		}
		q.filter = newProxyReader(q.stdout)
	}

	n, err := q.stdin.Write([]byte("?\n"))
	if err != nil {
		cancelFilter()
		return nil, err
	}
	if n != 2 {
		cancelFilter()
		return nil, errShortWrite
	}

	b, err := ioutil.ReadAll(q.filter)
	if len(b) == 0 {
		cancelFilter()
		return nil, errRequestRefused
	}
	return bytes.NewBuffer(b), err
}

type myHandler struct {
	port int
	conn *qrexecConnMultiplexer
}

func (m *myHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/forward" {
		w.WriteHeader(404)
		fmt.Fprintf(w, "the only valid URI in this service is /forward\n")
		return
	}
	uPort := r.URL.Query().Get("port")
	uVM := r.URL.Query().Get("target")

	port, err := strconv.Atoi(uPort)
	if port < 1025 || port > 65535 {
		w.WriteHeader(400)
		fmt.Fprintf(w, "port is a mandatory query string parameter and it cannot be outside the 1025-65535 range\n")
		return
	}

	VMre := regexp.MustCompile("[a-zA-Z0-9_-]{1,32}")
	if !VMre.MatchString(uVM) {
		w.WriteHeader(400)
		fmt.Fprintf(w, "target is a mandatory query string parameter and it must conform to the standards of VM names\n")
		return
	}
	vm := uVM

	log.Printf("Incoming metrics request for exporter on port %v in VM %v", port, vm)
	reader, err := m.conn.query(vm, port)
	if err != nil {
		log.Printf("Metrics query for exporter on port %v in VM %v failed: %s", port, vm, err)
		if err == errRequestRefused {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(500)
		}
		fmt.Fprintf(w, "%s\n", err)
		return
	}
	w.Header().Add("Content-Type", "text/plain; version=0.0.4")
	_, err = io.Copy(w, reader)
	if err != nil {
		log.Printf("Metrics read/write failed: %s", err)
		return
	}
}

var port = flag.Int("port", 8199, "Which port to listen on.")
var targetVm = "dom0"

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
	}
	log.Printf("Starting to serve on address %s", addr)
	log.Fatal(s.ListenAndServe())
}
