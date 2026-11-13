package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// baseURL builds the tcm server's address from a -port flag value.
func baseURL(port int) string {
	return fmt.Sprintf("http://%s:%d", wire.ServerHost, port)
}

func defaultServerPort() int {
	port, err := strconv.Atoi(os.Getenv("TCM_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return wire.ServerPort
	}
	return port
}
