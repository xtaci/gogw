package aiohttp

import (
	"container/list"
	"errors"
	"net"

	"github.com/xtaci/gaio"
)

var (
	ErrAlreadyDelegated = errors.New("already delegated")
)

type RequestContent []byte

// Delegated Request defines a single request to backend service
type DelegatedRequest struct {
	req         RequestContent
	chCompleted chan []byte
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
	queues  map[net.Conn]*list.List
}

func NewDelegationProxy() *DelegationProxy {
	proxy := new(DelegationProxy)
	proxy.queues = make(map[net.Conn]*list.List)
	return proxy
}

// Delegate queues a request for sequential accessing
func (proxy *DelegationProxy) Delegate(remote net.Conn, request []byte, chCompleted chan []byte) error {
	if remote == nil {
		panic("nil conn")
	}

	// queue exists, enqueue
	var q *list.List
	var ok bool
	if q, ok = proxy.queues[remote]; !ok {
		proxy.queues[remote] = list.New()
	}

	// create delegated request
	dr := new(DelegatedRequest)
	dr.req = request
	dr.chCompleted = chCompleted
	q.PushBack(dr)

	return nil
}

func (proxy *DelegationProxy) Start() {

}
