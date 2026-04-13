package main

import (
	"context"
	"log"
)

func main() {
	if err := runCLI(context.Background(), log.Writer()); err != nil {
		log.Fatal(err)
	}
}
