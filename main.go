// plugin-hongguo is an independent protocol-v2 source process.
package main

import (
	"fmt"
	"github.com/shi747826/mygo-plugin-hongguo/internal/sourceplugins"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"os"
	"strings"
)

func run() error {
	dir := os.Getenv("MYGO_PLUGIN_DATA_DIR")
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("MYGO_PLUGIN_DATA_DIR is required")
	}
	source, err := sourceplugins.NewHongguo(dir, os.Getenv("MYGO_PLAYER_FFMPEG"))
	if err != nil {
		return err
	}
	defer source.Close()
	return pluginapi.Serve(os.Stdin, os.Stdout, source.Handle)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
