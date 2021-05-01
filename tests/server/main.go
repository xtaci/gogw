package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"strconv"

	"github.com/xtaci/aiohttp"
)

func handler(ctx *aiohttp.AIOHttpContext) {
	switch string(ctx.URI.Path()) {
	case "/":
		ctx.Response.SetStatusCode(200)
		ctx.ResponseData = []byte("AIOHTTP")
	default:
		ctx.Response.SetStatusCode(404)
		ctx.ResponseData = []byte("Not Found")
	}
}

const (
	WORKER       = "AIOHTTP-WORKER"
	WORKER_IDENT = "AIOHTTP-WORKER-WORKER"
)

func main() {
	const numServer = 4
	worker := os.Getenv(WORKER)
	if worker == "" {
		go func() {
			log.Println(http.ListenAndServe(":6060", nil))
		}()

		var cmds []*exec.Cmd
		os.Setenv(WORKER, "1")
		for i := 0; i < numServer; i++ {
			os.Setenv(WORKER_IDENT, fmt.Sprint(i))
			cmd := exec.Command(os.Args[0])
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Start()
			cmds = append(cmds, cmd)
		}

		for k := range cmds {
			cmds[k].Wait()
		}
	} else if worker == "1" {
		workernumber, err := strconv.Atoi(os.Getenv(WORKER_IDENT))
		if err != nil {
			panic(err)
		}
		aiohttp.ListenAndServe(":8080", workernumber, 256*1024*1024, handler)
	}
}
