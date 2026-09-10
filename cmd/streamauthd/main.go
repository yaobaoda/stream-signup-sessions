package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	streamauth "github.com/infrai-examples/stream-signup-sessions"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	cdn := flag.String("cdn", "https://cdn.example/stream", "base URL renditions are delivered from")
	flag.Parse()

	gate, err := streamauth.NewCaptchaGate()
	if err != nil {
		log.Println(err)
		os.Exit(2)
	}

	svc := streamauth.NewService(gate, *cdn)
	log.Printf("streamauthd listening on %s", *addr)
	if err := http.ListenAndServe(*addr, svc.Routes()); err != nil {
		log.Fatal(err)
	}
}
