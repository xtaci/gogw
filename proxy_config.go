package aiohttp

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

type proxyService struct {
	regexp  *regexp.Regexp // regexp test
	address string         // ip:Port
}

// ProxyConfig is a proxy to manage remote services
type ProxyConfig struct {
	services []proxyService
}

// pattern match a URI
func (config *ProxyConfig) Match(uri *URI) (service string, valid bool) {
	for k := range config.services {
		if config.services[k].regexp.Match(uri.Path()) {
			return config.services[k].address, true
		}
	}
	return "", false
}

func LoadProxyConfig(path string) (*ProxyConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fileReader := bufio.NewReader(file)
	return ParseProxyConfig(fileReader)
}

// Parse proxy config data
// ip:port
// line 1: regex matching
// line 2: address:port
// ...
// line m: regex matching
// line n: addres:port
func ParseProxyConfig(reader io.Reader) (*ProxyConfig, error) {
	proxyConfig := new(ProxyConfig)

	var lineNum int

	// start by reading regex line
	readRegex := true
	bufferedReader := bufio.NewReader(reader)

	// parse rules line by line
	var rexp *regexp.Regexp
	for {
		line, err := bufferedReader.ReadString('\n')
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if readRegex {
			// parse pattern matching
			rexp, err = regexp.Compile(line)
			if err != nil {
				return nil, errors.Errorf("regexLimiter cannot parse regexp:line %d, data:%v, error: %v", lineNum, line, err)
			}
			readRegex = false
		} else {
			// parse address
			address := strings.TrimSpace(line)
			readRegex = true

			// add rules
			proxyConfig.services = append(proxyConfig.services, proxyService{rexp, address})
		}
	}

	return proxyConfig, nil
}
