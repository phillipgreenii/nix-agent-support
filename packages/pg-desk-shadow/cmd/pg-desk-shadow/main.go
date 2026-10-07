package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-desk-shadow/internal/report"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "quick" {
		if err := report.SelfTest(os.Stdout); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
}
