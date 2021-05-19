package aiohttp

import (
	reuse "github.com/libp2p/go-reuseport"
	"github.com/xtaci/gaio"
)

// Server defines gaio based http server
type Server struct {
	// addr optionally specifies the TCP address for the server to listen on,
	// in the form "host:port". If empty, ":http" (port 80) is used.
	// The service names are defined in RFC 6335 and assigned by IANA.
	// See net.Dial for details of the address format.
	addr    string
	proc    *AsyncHttpProcessor // I/O processor
	watcher *gaio.Watcher
}

// NewServer creates a gaio basd http server
// bufSize specify the swap buffer size for gaio.Watcher
// handler specify the http request handler
func NewServer(addr string, bufSize int, handler IRequestHandler, limiter IRequestLimiter) (*Server, error) {
	if handler == nil {
		return nil, ErrRequestHandlerEmpty
	}

	if bufSize <= 0 {
		return nil, ErrWatcherBufSize
	}

	watcher, err := gaio.NewWatcherSize(bufSize)
	if err != nil {
		return nil, err
	}
	proc := NewAsyncHttpProcessor(watcher, handler, limiter)
	server := &Server{addr: addr, proc: proc, watcher: watcher}
	return server, nil
}

// SetLoopAffinity binds the loop for reading/writing to a specific CPU
func (srv *Server) SetLoopAffinity(cpuid int) error {
	return srv.watcher.SetLoopAffinity(cpuid)
}

// SetPoolerAffinity binds the pooler, like kqueue/epoll to wait for events to a specific CPu
func (srv *Server) SetPoolerAffinity(cpuid int) error {
	return srv.watcher.SetPollerAffinity(cpuid)
}

// ListenAndServe starts listen and serve http requests
func (srv *Server) ListenAndServe() error {
	addr := srv.addr
	if addr == "" {
		addr = ":http"
	}

	ln, err := reuse.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// start processor loop
	srv.proc.StartProcessor()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}

		err = srv.proc.AddConn(conn)
		if err != nil {
			return err
		}
	}
}
