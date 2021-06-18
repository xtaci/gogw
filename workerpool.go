package aiohttp

import (
	"net"

	"github.com/xtaci/gaio"
)

type ServeHandler func(c net.Conn) error

// Delegation Proxy delegates a special conn to remote,
// and redirect it's IO to original connection:
//
//
//  Client Connection <- Delegation Proxy -> Delegation Connection
//
//
type DelegationProxy struct {
	watcher *gaio.Watcher
	handler ServeHandler
	pairs   map[net.Conn]net.Conn
}

func NewDelegationProxy() *DelegationProxy {
	proxy := new(DelegationProxy)
	proxy.pairs = make(map[net.Conn]net.Conn)
	return proxy
}

func (proxy *DelegationProxy) Delegate(client net.Conn, remote net.Conn) {
}
