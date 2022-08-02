package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
var errShortRead = errors.New("short read")

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
		defer func() {
			q.m.Unlock()
		}()
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
	targetVm      string
	targetPort    int
	cmd           *exec.Cmd
	cancel        context.CancelFunc
	cancelCommand context.CancelFunc
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	m             sync.Mutex
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

		arg := fmt.Sprintf("ruddo.PrometheusProxy+%v", q.targetPort)
		log.Printf("Launching qrexec-client-vm against %s with argument %s.", q.targetVm, arg)
		c := exec.CommandContext(ctx, "qrexec-client-vm", q.targetVm, arg)
		q.cmd = c

		var err error
		q.stdin, err = c.StdinPipe()
		if err != nil {
			log.Printf("%d: failed to launch program stdin pipe: %s.", q.targetPort, err)
			q.cancel()
			return nil, err
		}

		q.stdout, err = c.StdoutPipe()
		if err != nil {
			log.Printf("%d: failed to launch program stdout pipe: %s.", q.targetPort, err)
			q.cancel()
			return nil, err
		}
		c.Stderr = os.Stderr

		err = c.Start()
		if err != nil {
			log.Printf("%d: failed to start program.", q.targetPort)
			q.cancel()
			return nil, err
		}

		wrt, err := q.stdin.Write([]byte("+\n"))
		if err != nil {
			log.Printf("%d: failed to write initial handshake: %s.", q.targetPort, err)
			q.cancel()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if wrt != 2 {
			log.Printf("%d: failed to write initial handshake (short write of %d bytes).", q.targetPort, wrt)
			q.cancel()
			return nil, errRequestRefused
		}
		log.Println("Initial handshake successful")

		var mybuf [2]byte
		n, err := q.stdout.Read(mybuf[:])
		if err != nil {
			log.Printf("%d: failed to read initial handshake: %s.", q.targetPort, err)
			q.cancel()
			if err == io.EOF {
				err = errRequestRefused
			}
			return nil, err
		}
		if n != 2 || string(mybuf[:]) != "=\n" {
			log.Printf("%d: failed to read initial handshake (wrong contents).", q.targetPort)
			q.cancel()
			return nil, errRequestRefused
		}
	}

	n, err := q.stdin.Write([]byte("?\n"))
	if err != nil {
		log.Printf("%d: failed to write handshake: %s.", q.targetPort, err)
		q.cancel()
		return nil, err
	}
	if n != 2 {
		q.cancel()
		log.Printf("%d: failed to write handshake (short write).", q.targetPort)
		return nil, errShortWrite
	}

	readSoFar := []byte{}
	readLength := false
	for i := 0; i < 8; i++ { // max 9999999 bytes following read
		var buf [1]byte
		n, err := io.ReadFull(q.stdout, buf[:])
		if i == 0 && n == 0 {
			log.Printf("%d: failed to read length byte (nothing read).", q.targetPort)
			q.cancel()
			return nil, errShortRead
		}
		if err != nil {
			log.Printf("%d: failed to read length byte: %s.", q.targetPort, err)
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
		log.Printf("%d: data returned from exporter is too big", q.targetPort)
		q.cancel()
		return nil, fmt.Errorf("number too big")
	}

	length, err := strconv.Atoi(string(readSoFar))
	if err != nil || length < 1 {
		log.Printf("%d: data returned from exporter is too small: %s", q.targetPort, err)
		q.cancel()
		return nil, fmt.Errorf("number too small")
	}

	b := make([]byte, length)
	_, err = io.ReadFull(q.stdout, b)
	if err != nil {
		log.Printf("%d: failed to read payload from exporter: %s", q.targetPort, err)
		q.cancel()
		return nil, err
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
		IdleTimeout:    1,
	}
	log.Printf("Starting to serve on address %s", addr)
	log.Fatal(s.ListenAndServe())
}
