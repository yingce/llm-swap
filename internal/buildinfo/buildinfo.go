package buildinfo

import (
	"runtime/debug"

	"llm-swap/internal/protocol"
)

const AgentVersion = "2026.07.06.2"

var (
	Version   = ""
	Commit    = ""
	BuildTime = ""
)

func Current(protocolVersion int) protocol.BuildInfo {
	version := Version
	if version == "" {
		version = AgentVersion
	}
	info := protocol.BuildInfo{
		Version:         version,
		Commit:          Commit,
		BuildTime:       BuildTime,
		ProtocolVersion: protocolVersion,
	}
	if info.Commit == "" {
		info.Commit = vcsRevision()
	}
	return info
}

func vcsRevision() string {
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
