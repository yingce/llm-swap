//go:build linux

package agent

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func ReadAgentTokenFileHex(path string) (string, error) {
	return readAgentTokenFileHex(path, nil)
}

func readAgentTokenFileHex(path string, afterOpen func()) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", errInvalidAgentTokenFile
	}
	file := os.NewFile(uintptr(fd), "agent-token")
	if file == nil {
		_ = unix.Close(fd)
		return "", errInvalidAgentTokenFile
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", errInvalidAgentTokenFile
	}
	if afterOpen != nil {
		afterOpen()
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAgentTokenFileBytes+1))
	if err != nil {
		return "", errInvalidAgentTokenFile
	}
	return normalizeAgentTokenHex(data)
}
