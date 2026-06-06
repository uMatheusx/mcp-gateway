package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/uMatheusx/mcp-gateway/internal/config"
	"github.com/uMatheusx/mcp-gateway/internal/gateway"
	"github.com/uMatheusx/mcp-gateway/internal/mcp"
	"github.com/uMatheusx/mcp-gateway/internal/secrets"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "validate":
		runValidate(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("mcpgateway " + version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: mcpgateway <command> [options]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  serve     --config <path> --stdio    Start MCP stdio server")
	fmt.Fprintln(os.Stderr, "  validate  --config <path>            Validate a config file")
	fmt.Fprintln(os.Stderr, "  version                              Print version")
}

// ---- serve ----

func runServe(args []string) {
	var cfgPath string
	stdio := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--stdio":
			stdio = true
		}
	}

	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(1)
	}
	if !stdio {
		fmt.Fprintln(os.Stderr, "error: only --stdio mode is currently supported")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	resolver, err := secrets.NewResolver(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: initializing secret resolver: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(ctx, cfgPath, resolver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	gw := gateway.New(cfg)
	srv := mcp.NewStdioServer(cfg, gw)

	if err := srv.Serve(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ---- validate ----

func runValidate(args []string) {
	var cfgPath string
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
			i++
		}
	}

	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(1)
	}

	if _, err := config.Load(context.Background(), cfgPath, nil); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ %s is valid\n", cfgPath)
}
