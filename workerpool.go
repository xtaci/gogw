package aiohttp

import (
	"log"
	"net"

	"github.com/xtaci/gaio"
)

type RequestContent []byte

type DelegatedRequestContext struct {
	request     []byte
	chCompleted chan []byte
	client      net.Conn
	remote      net.Conn
}

// Delegation Proxy delegates a special conn to remote,
// and redirect it's IO to original connection:
//
//
//  Client Connection(URL Path) -> io.ReadWriter -> Delegation Proxy -> Delegation Connection
//
//
type DelegationProxy struct {
	watcher *gaio.Watcher
}

func NewDelegationProxy(watcher *gaio.Watcher) *DelegationProxy {
	proxy := new(DelegationProxy)
	proxy.watcher = watcher
	return proxy
}

// Delegate queues a request for sequential accessing
func (proxy *DelegationProxy) Delegate(client net.Conn, remote net.Conn, request []byte, chCompleted chan []byte) error {
	if remote == nil {
		panic("nil conn")
	}

	// create delegated request context
	dr := new(DelegatedRequestContext)
	dr.client = client
	dr.remote = remote
	dr.request = request
	dr.chCompleted = chCompleted

	// watcher
	err := proxy.watcher.Write(dr, remote, request)
	return err
}

func (proxy *DelegationProxy) Start() {
	go func() {
		for {
			// loop wait for any IO events
			results, err := proxy.watcher.WaitIO()
			if err != nil {
				log.Println(err)
				return
			}

			// context based req/resp matchintg
			for _, _ = range results {
			}
		}
	}()
}
