package gogw

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestIllegalProxyRequest(t *testing.T) {

	proxy, err := NewDelegationProxy(1024 * 1024)
	if err != nil {
		panic(err)
	}
	proxy.Start()

	request := `GET /debug/pprof/ HTTP/1.1
Host:127.0.0.1

`
	chResponse := make(chan []byte, 1)
	proxy.Delegate("localhost:6068", []byte(request), chResponse)

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

func TestProxyConfigParser(t *testing.T) {
	testString := `
/abc
127.0.0.1:6060

/a/.*/b

127.0.0.1:8080

/def
127.0.0.1:9090
`

	reader := strings.NewReader(testString)

	config, err := ParseProxyConfig(reader)
	assert.Nil(t, err)

	for k := range config.services {
		t.Logf("regexp:%v address:%v", config.services[k].regexp.String(), config.services[k].address)
	}

	var uri URI
	err = uri.Parse(nil, []byte("/abc/"))
	assert.Nil(t, err)
	address, valid := config.Match(&uri)
	assert.True(t, valid)
	t.Log(string(uri.Path()), valid, address)

	err = uri.Parse(nil, []byte("/def?adfadf=a"))
	assert.Nil(t, err)
	address, valid = config.Match(&uri)
	assert.True(t, valid)
	t.Log(string(uri.Path()), valid, address)
}
