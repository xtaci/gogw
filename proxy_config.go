package aiohttp

import (
	"io"
	"regexp"
)

type ProxyService struct {
	regexp  *regexp.Regexp // regexp test
	address string         // ip:Port
}

// ProxyConfig is a proxy to manage remote services
type ProxyConfig struct {
	services []ProxyService
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

func ParseProxyConfig(reader io.Reader) *ProxyConfig {
	return nil
}
