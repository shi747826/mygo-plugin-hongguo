// Command manifestgen refreshes the embedded, complete repository manifest.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: manifestgen input.json output.json")
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	if !json.Valid(raw) {
		panic(fmt.Errorf("invalid manifest JSON"))
	}
	if err := os.WriteFile(os.Args[2], raw, 0644); err != nil {
		panic(err)
	}
}
