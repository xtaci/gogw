package main

import (
	"errors"
	"log"
	"net/http"
	_ "net/http/pprof"
	"strings"

	"github.com/xtaci/aiohttp"
)

var (
	errPath = errors.New("incorrect path")
)

var (
	proxy       *aiohttp.DelegationProxy
	proxyConfig *aiohttp.ProxyConfig
)

var (
	asyncResponses chan []byte = make(chan []byte)
)

func handler(ctx *aiohttp.LocalContext) error {
	// parse URI
	var URI aiohttp.URI // current incoming request's URL
	err := URI.Parse(nil, ctx.Header.RequestURI())
	if err != nil {
		return err
	}

	// check if it's delegated URI
	if remote, ok := proxyConfig.Match(&URI); ok {
		proxy.Delegate(remote, ctx)
		return nil
	} else {
		path := string(URI.Path())
		// http route
		switch path {
		case "/":
			ctx.Response.SetStatusCode(200)
			ctx.ResponseData = []byte("AIOHTTP")
		}
		return nil
	}

	ctx.Response.SetStatusCode(404)
	ctx.ResponseData = []byte("Not Found")
	return errPath
}

func asyncResponseManager() {

}

func main() {
	const numServer = 4
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	testDelegates := `
/debug/pprof
127.0.0.1:6060
`

	var err error
	reader := strings.NewReader(testDelegates)
	proxyConfig, err = aiohttp.ParseProxyConfig(reader)
	proxy, err = aiohttp.NewDelegationProxy(1024 * 1024)
	if err != nil {
		panic(err)
	}
	proxy.Start()

	for i := 0; i < numServer; i++ {
		server, err := aiohttp.NewServer(":8080", 256*1024*1024, handler, nil)
		if err != nil {
			panic(err)
		}
		server.SetLoopAffinity(i * 2)
		server.SetPoolerAffinity(i * 2)
		go server.ListenAndServe()
	}

	select {}
}
