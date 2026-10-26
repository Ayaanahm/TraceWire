package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Ayaanahm/tracewire/internal/faults"
	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/orders"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

func main() {
	httpx.InitLogger("orders")
	t, err := telemetry.New(context.Background(), "orders")
	if err != nil {
		slog.Error("telemetry", "err", err)
		os.Exit(1)
	}
	svc := orders.New(t, httpx.Getenv("INVENTORY_URL", "http://localhost:8082"), telemetry.Client(2*time.Second))
	if err := httpx.Run(t, httpx.Getenv("ADDR", ":8081"), orders.NewMux(t, svc, faults.New())); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
