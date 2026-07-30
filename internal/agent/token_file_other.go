//go:build !linux

package agent

func ReadAgentTokenFileHex(string) (string, error) {
	return "", errInvalidAgentTokenFile
}
