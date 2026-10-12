package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/jheck90/gilgamesh/internal/adding"
	"github.com/jheck90/gilgamesh/internal/estimate"
	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/library"
	"github.com/jheck90/gilgamesh/internal/ocr"
	"github.com/jheck90/gilgamesh/internal/web"
)

func (a *app) serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOr("GMESH_ADDR", ":8080"), "address to listen on")
	trust := fs.String("trust-header", os.Getenv("GMESH_TRUST_HEADER"),
		"take the reader's name from this header set by an auth proxy (e.g. Remote-User); only when the app is reachable solely through that proxy")
	libraryDir := fs.String("library", os.Getenv("GMESH_LIBRARY"), "directory of EPUBs readers can pick a book to add from (mount it read-only)")
	pricesFile := fs.String("prices", envOr("GMESH_PRICES", ""), "JSON file of model prices the library page uses instead of the built-in ones")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	prices, err := estimate.LoadPrices(*pricesFile)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	bx, err := box()
	if err != nil {
		return err
	}
	var lib *library.Library
	if *libraryDir != "" {
		if info, err := os.Stat(*libraryDir); err != nil || !info.IsDir() {
			return fmt.Errorf("--library %s isn't a directory", *libraryDir)
		}
		lib = &library.Library{Root: *libraryDir, Store: st}
	}
	queue := &adding.Queue{Store: st, Box: bx, Env: keys.Env(os.Getenv), DBPath: a.dbPath, Log: log.Writer()}
	queueDone := make(chan struct{})
	go func() {
		defer close(queueDone)
		if err := queue.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("gmesh: adding books in the background stopped: %v", err)
		}
	}()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&web.Server{Store: st, TrustHeader: *trust, DefaultUser: a.userName, Photos: ocr.Tesseract{}, Adding: queue, Library: lib, Prices: prices}).Handler(),
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
	if lib != nil {
		log.Printf("gmesh: books can be added from the library in %s", lib.Root)
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		log.Printf("gmesh: snapping by photo won't work until tesseract is installed; typed words still will")
	}
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		// Let a book being added save its last call before the store closes.
		<-queueDone
		return nil
	}
	return err
}
