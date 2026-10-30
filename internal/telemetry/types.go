// Package telemetry owns bounded, ephemeral node observations. Pulse stores history.
package telemetry

import (
	"context"
	"time"
)

const Retention = 15 * time.Minute
const MaxRecords = 2048
const MaxBufferBytes = 4 << 20
const MaxRecordBytes = 64 << 10
const MaxPageSize = 128
const MaxPageBytes = 512 << 10

// Metric has either a gauge Value or an integer Counter; unavailable values are
// null. Reason is a safe code, never a raw filesystem, controller or SQL error.
type Metric struct {
	Name          string    `json:"name"`
	Scope         string    `json:"scope"`
	Resource      string    `json:"resource,omitempty"`
	Unit          string    `json:"unit"`
	Available     bool      `json:"available"`
	Value         *float64  `json:"value"`
	Counter       *uint64   `json:"counter,string"`
	Reason        string    `json:"reason,omitempty"`
	ObservedAt    time.Time `json:"observedAt"`
	WindowSeconds float64   `json:"windowSeconds,omitempty"`
}

type Check struct {
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	Scope           string    `json:"scope"`
	Vantage         string    `json:"vantage"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason,omitempty"`
	ObservedAt      time.Time `json:"observedAt"`
	RTTMilliseconds *float64  `json:"rttMilliseconds"`
	LossPercent     *float64  `json:"lossPercent"`
}

type Sample struct {
	RunID      string       `json:"runId"`
	Sequence   uint64       `json:"sequence,string"`
	Kind       string       `json:"kind"`
	ObservedAt time.Time    `json:"observedAt"`
	Metrics    []Metric     `json:"metrics,omitempty"`
	Checks     []Check      `json:"checks,omitempty"`
	Runtime    *RuntimeInfo `json:"runtime,omitempty"`
}

type RuntimeInfo struct {
	CoreState   string `json:"coreState"`
	CoreVersion string `json:"coreVersion,omitempty"`
	UserState   string `json:"userState"`
}

type Sampler interface{ Sample(context.Context) Sample }

type Filesystem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

type ResourceOptions struct {
	HostProc            string       `json:"hostProc"`
	Cgroup              string       `json:"cgroup"`
	HostInterfaces      []string     `json:"hostInterfaces"`
	ContainerInterfaces []string     `json:"containerInterfaces"`
	Disks               []string     `json:"disks"`
	Filesystems         []Filesystem `json:"filesystems"`
}

// Targets are deployment configuration, never supplied by HTTP callers.
type ProbeTarget struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Address string `json:"address"`
}

type ProbeOptions struct {
	Targets     []ProbeTarget `json:"targets"`
	Timeout     time.Duration `json:"-"`
	Concurrency int           `json:"concurrency"`
}
