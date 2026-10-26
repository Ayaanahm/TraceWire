package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Ayaanahm/tracewire/internal/console"
	"github.com/Ayaanahm/tracewire/internal/gateway"
	"github.com/Ayaanahm/tracewire/internal/httpx"
	"github.com/Ayaanahm/tracewire/internal/telemetry"
)

func main() {
	httpx.InitLogger("gateway")
	t, err := telemetry.New(context.Background(), "gateway")
	if err != nil {
		slog.Error("telemetry", "err", err)
		os.Exit(1)
	}
	addr := httpx.Getenv("ADDR", ":8080")
	rec := console.NewRecorder()
	mux := gateway.NewMux(t, httpx.Getenv("ORDERS_URL", "http://localhost:8081"), telemetry.Client(5*time.Second), rec.Record)
	console.Mount(mux, console.Options{
		Recorder:  rec,
		Traffic:   console.NewTraffic("http://localhost" + addr[strings.LastIndex(addr, ":"):] + "/api/orders"),
		FaultsURL: httpx.Getenv("INVENTORY_FAULTS_URL", "http://localhost:8082/admin/faults"),
		Controls:  os.Getenv("CONSOLE_CONTROLS") == "true",
		Client:    &http.Client{Timeout: 3 * time.Second},
		JaegerURL: os.Getenv("CONSOLE_JAEGER_URL"),
	})
	if err := httpx.Run(t, addr, mux); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
