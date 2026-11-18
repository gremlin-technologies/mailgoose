// Command mailgoose is the customer CLI. It talks to the mailgoose API and
// ships as a single static binary.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/gremlin-technologies/mailgoose/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.NewApp().Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
