package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jheck90/gilgamesh/internal/web"
)

func (a *app) serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOr("GMESH_ADDR", ":8080"), "address to listen on")
	trust := fs.String("trust-header", os.Getenv("GMESH_TRUST_HEADER"),
		"take the reader's name from this header set by an auth proxy (e.g. Remote-User); only when the app is reachable solely through that proxy")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&web.Server{Store: st, TrustHeader: *trust, DefaultUser: a.userName}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	mode := "single reader " + a.userName
	if *trust != "" {
		mode = "readers from the " + *trust + " header"
	}
	log.Printf("gmesh: serving %s on %s (%s)", a.dbPath, *addr, mode)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
