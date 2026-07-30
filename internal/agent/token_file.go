package agent

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
)

const maxAgentTokenFileBytes = 16384

var errInvalidAgentTokenFile = errors.New("invalid agent token file")

func normalizeAgentTokenHex(data []byte) (string, error) {
	if len(data) < 1 || len(data) > maxAgentTokenFileBytes || bytes.IndexByte(data, 0) >= 0 {
		return "", errInvalidAgentTokenFile
	}
	if data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
		if len(data) > 0 && data[len(data)-1] == '\r' {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 {
		return "", errInvalidAgentTokenFile
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errInvalidAgentTokenFile
	}
	return hex.EncodeToString([]byte(token)), nil
}
