package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, err := reconcile.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	s := &reconcile.Service{Store: store}
	total := 0
	for {
		n, err := s.ImportPage(ctx, os.Getenv("IMPORT_TENANT"), os.Getenv("IMPORT_SOURCE_ID"), os.Getenv("INDEXER_URL"), os.Getenv("INDEXER_SCOPE"))
		if err != nil {
			return err
		}
		total += n
		if n == 0 {
			break
		}
	}
	fmt.Printf("Imported %d finalized movements; no payment coverage inferred.\n", total)
	return nil
}
