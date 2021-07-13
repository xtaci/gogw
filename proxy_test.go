package aiohttp

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"testing"
)

func TestProxyRequest(t *testing.T) {
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

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
