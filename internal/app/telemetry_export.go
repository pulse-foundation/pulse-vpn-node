package app

import (
	"context"
	"sync"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

type telemetryDelivery struct {
	mu                 sync.Mutex
	active             *telemetry.Exporter
	configurationError string
}

func (d *telemetryDelivery) Status() telemetry.ExportStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active != nil {
		return d.active.Status()
	}
	return telemetry.ExportStatus{ConfigurationError: d.configurationError, CommittedSequence: "0", HeartbeatSequence: "0"}
}
func startTelemetryDelivery(ctx context.Context, path string, buffer *telemetry.Buffer, options telemetry.ExporterOptions) (*telemetryDelivery, func()) {
	delivery := &telemetryDelivery{}
	worker, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); delivery.run(worker, path, buffer, options) }()
	return delivery, func() { cancel(); <-done }
}
func (d *telemetryDelivery) run(ctx context.Context, path string, buffer *telemetry.Buffer, options telemetry.ExporterOptions) {
	var installed *telemetry.ExportConfig
	var stop context.CancelFunc
	var finished chan struct{}
	stopActive := func() {
		if stop != nil {
			stop()
			<-finished
			stop = nil
		}
		d.mu.Lock()
		d.active = nil
		d.mu.Unlock()
	}
	defer stopActive()
	for ctx.Err() == nil {
		cfg, err := config.LoadTelemetryExport(path)
		d.mu.Lock()
		if err == nil {
			d.configurationError = ""
		}
		d.mu.Unlock()
		changed := (cfg == nil) != (installed == nil) || (cfg != nil && installed != nil && *cfg != *installed)
		if err != nil || changed {
			stopActive()
			installed = nil
			d.mu.Lock()
			d.configurationError = ""
			if err != nil {
				d.configurationError = "invalid_enrollment"
			}
			d.mu.Unlock()
			if err == nil && cfg != nil {
				exporter, createErr := telemetry.NewExporter(*cfg, buffer, options)
				if createErr == nil {
					owned, cancel := context.WithCancel(ctx)
					stop = cancel
					finished = make(chan struct{})
					completion := finished
					installed = cfg
					d.mu.Lock()
					d.active = exporter
					d.mu.Unlock()
					go func() { defer close(completion); exporter.Run(owned) }()
				} else {
					d.mu.Lock()
					d.configurationError = "invalid_enrollment"
					d.mu.Unlock()
				}
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
