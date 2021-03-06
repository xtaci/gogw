package main

import (
	"errors"
	"log"
	"net/http"
	_ "net/http/pprof"
	"strings"

	"github.com/xtaci/gogw"
)

var (
	errPath = errors.New("incorrect path")
)

var (
	proxy       *gogw.DelegationProxy
	proxyConfig *gogw.ProxyConfig
)

var (
	dummyData = make([]byte, 16)
)

func handler(ctx *gogw.BaseContext) error {
	// parse URI
	var URI gogw.URI // current incoming request's URL
	err := URI.Parse(nil, ctx.Header.RequestURI())
	if err != nil {
		return err
	}

	// check if it's delegated URI
	if remote, ok := proxyConfig.Match(&URI); ok {
		dummy := func(ctx *gogw.RemoteContext) error {
			//	log.Println(string(ctx.RespHeader.Header()))
			ctx.RespHeader.Add("a", "b")
			ctx.RespData = []byte("hello")
			ctx.RespHeader.SetContentLength(len(ctx.RespData))
			return nil
		}

		proxy.Delegate(remote, ctx, dummy)
		return nil
	} else {
		path := string(URI.Path())
		// http route
		switch path {
		case "/":
			ctx.Response.SetStatusCode(200)
			//ctx.ResponseData = []byte("AIOHTTP")
			ctx.ResponseData = dummyData
		}
		return nil
	}

	ctx.Response.SetStatusCode(404)
	ctx.ResponseData = []byte("Not Found")
	return errPath
}

func main() {

	for k := 0; k < len(dummyData); k++ {
		dummyData[k] = 'A'
	}
	const numServer = 4
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	testDelegates := `
/debug/pprof
127.0.0.1:6060

/post.*
127.0.0.1:8080
`

	var err error
	reader := strings.NewReader(testDelegates)
	proxyConfig, err = gogw.ParseProxyConfig(reader)
	proxy, err = gogw.NewDelegationProxy(1024 * 1024)
	if err != nil {
		panic(err)
	}
	proxy.Start()

	for i := 0; i < numServer; i++ {
		server, err := gogw.NewServer(":8081", 256*1024*1024, handler, nil)
		if err != nil {
			panic(err)
		}
		server.SetLoopAffinity(i * 2)
		server.SetPoolerAffinity(i * 2)
		go server.ListenAndServe()
	}

	select {}
}
