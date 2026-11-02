package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

const purgeUsage = "usage: api purge --before RFC3339 [--limit 100] [--confirm TOKEN]"

func parsePurge(args []string) (storage.PurgeOptions, error) {
	var o storage.PurgeOptions
	flags := flag.NewFlagSet("purge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	before := flags.String("before", "", "exclusive cutoff (RFC3339)")
	flags.IntVar(&o.Limit, "limit", 100, "maximum incidents in one batch (1-1000)")
	flags.StringVar(&o.Confirm, "confirm", "", "confirmation token from a preview; enables deletion")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 {
		return o, errors.New(purgeUsage)
	}
	var err error
	o.Before, err = time.Parse(time.RFC3339Nano, *before)
	if err != nil {
		return o, errors.New("before must be a non-future RFC3339 timestamp")
	}
	return o, o.Validate()
}

func purgeCommand(args []string, databaseURL string, out io.Writer) error {
	o, err := parsePurge(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, purgeUsage+"\nPreview by default. --confirm TOKEN deletes the matching preview (limit: 1-1000).")
		return err
	}
	if err != nil {
		return err
	}
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store, err := storage.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Purge(ctx, o)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		if result.Deleted {
			return fmt.Errorf("purge committed; writing result failed: %w", err)
		}
		return fmt.Errorf("writing purge preview: %w", err)
	}
	return nil
}
