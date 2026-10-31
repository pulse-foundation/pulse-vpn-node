// Package buildinfo reports the identity embedded before image verification.
package buildinfo

import "runtime"

var version = "dev"
var commit = "unknown"

type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Architecture string `json:"architecture"`
}

func Current() Info { return Info{Version: version, Commit: commit, Architecture: runtime.GOARCH} }
