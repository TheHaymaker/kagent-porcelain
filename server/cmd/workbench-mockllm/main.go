// Command workbench-mockllm is a scripted model for the end-to-end smoke
// test. It speaks the OpenAI Chat Completions API (kagent's mockllm library,
// the same one kagent's own end-to-end tests use) and always answers the
// smoke prompt by calling workbench_request_promotion, so the test gets a
// real kagent pause of kind production deploy without a real model.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kagent-dev/mockllm"
)

//go:embed script.json
var script []byte

func main() {
	var cfg mockllm.Config
	if err := json.Unmarshal(script, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "script:", err)
		os.Exit(1)
	}
	cfg.ListenAddr = ":8080"
	if a := os.Getenv("LISTEN_ADDR"); a != "" {
		cfg.ListenAddr = a
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := mockllm.NewServer(cfg)
	url, err := srv.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	fmt.Println("scripted model listening at", url)
	<-ctx.Done()
	_ = srv.Stop(context.Background())
}
