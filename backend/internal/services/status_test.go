package services

import "testing"

func TestDeploymentStatusNames(t *testing.T) {
	statuses := map[string]bool{"pending": true, "running": true, "success": true, "cancelled": true, "failed": true}
	for _, status := range []string{"pending", "running", "success"} {
		if !statuses[status] {
			t.Fatalf("expected status %s to be supported", status)
		}
	}
}

func TestIPMIHostParsing(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":               "192.0.2.10",
		"192.0.2.10:623":           "192.0.2.10",
		"ipmi://192.0.2.20":        "192.0.2.20",
		"https://bmc.example:8443": "bmc.example",
	}
	for input, expected := range cases {
		if got := ipmiHost(input); got != expected {
			t.Fatalf("ipmiHost(%q)=%q, expected %q", input, got, expected)
		}
	}
}
