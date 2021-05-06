package aiohttp

import (
	reuse "github.com/libp2p/go-reuseport"
	"github.com/xtaci/gaio"
)

type Server struct {
	// addr optionally specifies the TCP address for the server to listen on,
	// in the form "host:port". If empty, ":http" (port 80) is used.
	// The service names are defined in RFC 6335 and assigned by IANA.
	// See net.Dial for details of the address format.
	addr    string
	Proc    *AIOHttpProcessor // I/O processor
	watcher *gaio.Watcher
}

func NewServer(addr string, bufSize int, handler RequestHandler) (*Server, error) {
	watcher, err := gaio.NewWatcherSize(bufSize)
	if err != nil {
		return nil, err
	}
	proc := NewAIOHttpProcessor(watcher, handler)
	server := &Server{addr: addr, Proc: proc, watcher: watcher}
	return server, nil
}

func (srv *Server) SetLoopAffinity(cpuid int) error {
	return srv.watcher.SetLoopAffinity(cpuid)
}

func (srv *Server) SetPoolerAffinity(cpuid int) error {
	return srv.watcher.SetPollerAffinity(cpuid)
}

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
	srv.Proc.StartProcessor()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}

		err = srv.Proc.AddConn(conn)
		if err != nil {
			return err
		}
	}
}
