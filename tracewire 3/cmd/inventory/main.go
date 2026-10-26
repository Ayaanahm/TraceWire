package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/Ayaanahm/tracewire/internal/faults"
	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/inventory"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

func main() {
	httpx.InitLogger("inventory")
	t, err := telemetry.New(context.Background(), "inventory")
	if err != nil {
		slog.Error("telemetry", "err", err)
		os.Exit(1)
	}
	mux := inventory.NewMux(t, inventory.NewStore(inventory.DefaultSeed()), faults.New())
	if err := httpx.Run(t, httpx.Getenv("ADDR", ":8082"), mux); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
