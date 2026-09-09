// Command principal-store serves the principal registry on 127.0.0.1:8314.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	principalstore "github.com/kayushkin/principal-store"
)

func main() {
	addr := os.Getenv("PRINCIPAL_STORE_ADDR")
	if addr == "" {
		// Loopback, deliberately — not ":8314" like the older siblings. This
		// service has no auth of its own; dash is the front door that adds it.
		// A wildcard bind would put principal editing on the network for
		// anything that can route to this host.
		addr = "127.0.0.1:8314"
	}
	dataDir := os.Getenv("PRINCIPAL_STORE_DATA_DIR")

	store, err := principalstore.Open(dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	mux := http.NewServeMux()
	principalstore.RegisterHandlers(mux, store)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("principal-store listening on %s (data=%s)", addr, store.DataDir())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
