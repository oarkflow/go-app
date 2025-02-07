package main

import (
	"fmt"
	"os"
	"time"

	"github.com/oarkflow/dag/bcl"
)

func main() {
	doc, err := bcl.Parse([]byte(config), "server.conf")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", os.Args[0], err)
		os.Exit(1)
	}
	fmt.Println(doc.Print())
}

var config = `
net {
    listen ":https"

    tls {
        cert "/var/lib/ssl/server.crt"
        key  "/var/lib/ssl/server.key"

        ciphers ["AES-128SHA256", "AES-256SHA256"]
    }
}

log access {
    level "info"
    file  "/var/log/http/access.log"
}

body_limt 50MB

timeout {
    read  10m
    write 10m
}
`

type Config struct {
	Net struct {
		Listen string

		TLS struct {
			Cert    string
			Key     string
			Ciphers []string
		}
	}

	Log map[string]struct {
		Level string
		File  string
	}

	BodyLimit int64 `config:"body_limit"`

	Timeout struct {
		Read  time.Duration
		Write time.Duration
	}
}
