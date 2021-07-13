package aiohttp

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"testing"
)

func init() {
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()
}

func TestProxyRequest(t *testing.T) {

	proxy, err := NewDelegationProxy(1024 * 1024)
	if err != nil {
		panic(err)
	}
	proxy.Start()

	request := `GET /debug/pprof/ HTTP/1.1
Host:127.0.0.1

`
	chResponse := make(chan []byte, 1)
	proxy.Delegate("localhost:6060", []byte(request), chResponse)

	t.Log(string(<-chResponse))
}

func BenchmarkProxyRequest(b *testing.B) {
	proxy, err := NewDelegationProxy(16 * 1024 * 1024)
	if err != nil {
		panic(err)
	}
	proxy.Start()

	request := `GET /debug/pprof/ HTTP/1.1
Host:127.0.0.1

`

	n := b.N
	chResponse := make(chan []byte, b.N)
	go func() {
		for j := 0; j < n; j++ {
			<-chResponse
		}
	}()

	for i := 0; i < n; i++ {
		proxy.Delegate("localhost:6060", []byte(request), chResponse)
	}
}
