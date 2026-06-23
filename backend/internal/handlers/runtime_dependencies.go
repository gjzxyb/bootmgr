package handlers

import (
	"os/exec"
	"strings"
)

var lookExecutable = exec.LookPath

func bmcToolingStatus(adapter string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(adapter)) {
	case "ipmi":
		path, err := lookExecutable("ipmitool")
		if err != nil {
			return "error", "BMC_ADAPTER=ipmi requires ipmitool in PATH"
		}
		return "ok", "ipmitool is available at " + path
	case "redfish":
		return "ok", "BMC_ADAPTER=redfish uses built-in HTTP client"
	case "simulated", "":
		return "ok", "BMC_ADAPTER does not require external tooling"
	default:
		return "error", "unsupported BMC_ADAPTER"
	}
}
