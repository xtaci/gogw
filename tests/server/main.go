package main

import (
	"errors"
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/xtaci/aiohttp"
)

var (
	errPath = errors.New("incorrect path")
)

var (
	delegatedURI map[string]string = map[string]string{
		"/remote": "http://localhost:6060",
	}

	proxy *aiohttp.DelegationProxy
)

func handler(ctx *aiohttp.AIOContext) error {
	// parse URI
	var URI aiohttp.URI // current incoming request's URL
	err := URI.Parse(nil, ctx.Header.RequestURI())
	if err != nil {
		return err
	}

	// check if it's delegated URI
	path := string(URI.Path())
	if remote, ok := delegatedURI[path]; ok {
		// TODO: delegates to proxy
		//proxy.Delegate(
		_ = remote
		return nil
	} else {
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

func main() {
	const numServer = 4
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

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
