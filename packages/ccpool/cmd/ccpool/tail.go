package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

func runTail(args []string) int {
	fs := flag.NewFlagSet("tail", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool tail <external_id>")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		return 1
	}
	defer st.Close()
	row, ok, err := st.GetByExternalID(context.Background(), fs.Arg(0))
	if err != nil || !ok || row.TranscriptPath == "" {
		fmt.Fprintln(os.Stderr, "no transcript for", fs.Arg(0))
		return 1
	}
	f, err := os.Open(row.TranscriptPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open transcript:", err)
		return 1
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			var ev struct {
				Type    string `json:"type"`
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "assistant" {
				for _, b := range ev.Message.Content {
					if b.Type == "text" && b.Text != "" {
						fmt.Println(b.Text)
					}
				}
			}
		}
		if err != nil { // EOF: poll for more (follow)
			time.Sleep(500 * time.Millisecond)
		}
	}
}
