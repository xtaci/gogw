package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/xtaci/aiohttp"
)

func main() {
	const numServer = 2
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	for i := 0; i < numServer; i++ {
		go aiohttp.ListenAndServe(":8080", i, 256*1024*1024)
	}

	select {}
}
