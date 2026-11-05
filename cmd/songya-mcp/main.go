package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rootsFlag map[string]string

func (r rootsFlag) String() string { return "name=/absolute/source/path" }
func (r rootsFlag) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("use name=/source/path")
	}
	r[name] = path
	return nil
}
func main() {
	flags := flag.NewFlagSet("songya-mcp", flag.ExitOnError)
	flags.SetOutput(os.Stderr)
	storePath := flags.String("store", "", "authoritative memory store directory (required)")
	configPath := flags.String("config", "", "configuration file (default: STORE/config.json)")
	roots := rootsFlag{}
	flags.Var(roots, "import-root", "optional named import root; repeat for more roots")
	_ = flags.Parse(os.Args[1:])
	if *storePath == "" {
		flags.Usage()
		os.Exit(2)
	}
	path := *configPath
	if path == "" {
		path = filepath.Join(*storePath, "config.json")
	}
	var config struct {
		Embeddings songya.EmbeddingConfig `yaml:"embeddings"`
	}
	if data, err := os.ReadFile(path); err == nil {
		if yaml.Unmarshal(data, &config) != nil {
			log.Print("Embedding configuration needs repair; readable values and defaults remain usable")
		}
	} else if !os.IsNotExist(err) {
		log.Print("Embedding configuration unavailable; using defaults")
	}
	store, err := songya.Open(*storePath, config.Embeddings)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := mcpserver.New(ctx, store, roots)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
