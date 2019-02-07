package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

type qrexecConn struct {
	targetVm string
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	filter   io.Reader
	m        sync.Mutex
}

func (q *qrexecConn) query() (io.Reader, error) {
	q.m.Lock()
	defer q.m.Unlock()
	if q.cmd == nil {
		ctx, cancel := context.WithCancel(context.Background())
		q.cancel = cancel
		c := exec.CommandContext(ctx, "qrexec-client-vm", q.targetVm, "ruddo.PrometheusProxy")
		q.cmd = c
		var err error
		q.stdin, err = c.StdinPipe()
		if err != nil {
			q.cmd = nil
			q.cancel = nil
			q.stdin = nil
			q.stdout = nil
			return nil, err
		}
		q.stdout, err = c.StdoutPipe()
		if err != nil {
			q.cmd = nil
			q.cancel = nil
			q.stdin = nil
			q.stdout = nil
			return nil, err
		}
		c.Stderr = os.Stderr
		err = c.Start()
		if err != nil {
			q.cmd = nil
			q.cancel = nil
			q.stdin = nil
			q.stdout = nil
			return nil, err
		}
		q.filter = newProxyReader(q.stdout)
	}

	_, err := q.stdin.Write([]byte("?\n"))
	if err != nil {
		q.cancel()
		q.stdin.Close()
		q.stdout.Close()
		q.cmd = nil
		q.cancel = nil
		q.stdin = nil
		q.stdout = nil
		q.filter = nil
		return nil, err
	}
	b, err := ioutil.ReadAll(q.filter)
	return bytes.NewBuffer(b), err
}

type myHandler struct {
	port int
	conn *qrexecConn
}

func (m *myHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		w.WriteHeader(404)
		return
	}
	log.Printf("Incoming metrics request")
	reader, err := m.conn.query()
	if err != nil {
		log.Printf("Metrics query failed: %s", err)
		w.WriteHeader(500)
		fmt.Fprintf(w, "%s", err)
		return
	}
	w.Header().Add("Content-Type", "text/plain; version=0.0.4")
	_, err = io.Copy(w, reader)
	if err != nil {
		log.Printf("Metrics read/write failed: %s", err)
		return
	}
}

var port = 9100
var targetVm = "dom0"

func main() {
	addr := fmt.Sprintf(":%d", port)
	c := &qrexecConn{
		targetVm: targetVm,
	}
	s := &http.Server{
		Addr:           addr,
		Handler:        &myHandler{port, c},
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 10,
	}
	log.Printf("Starting to serve on address %s", addr)
	log.Fatal(s.ListenAndServe())
}
