package orchestrator

import (
	"encoding/json"
	"strings"
)

func jsonMarshal(req RunRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonUnmarshal(raw string) (RunRequest, error) {
	var req RunRequest
	err := json.Unmarshal([]byte(raw), &req)
	return req, err
}

func isBusyGroup(err error) bool {
	return strings.Contains(err.Error(), "BUSYGROUP")
}
