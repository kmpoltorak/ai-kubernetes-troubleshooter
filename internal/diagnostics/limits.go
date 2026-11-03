package diagnostics

import "fmt"

// SamplingLimits describes configured bounds, not a claim that each was reached.
func SamplingLimits() []string {
	return []string{
		"Diagnostics are bounded snapshots, not a complete audit or proof of application health.",
		fmt.Sprintf("Pod details: at most %d pods, unhealthy first.", maxPods),
		fmt.Sprintf("Events: at most %d recent related events; matched by name, not UID; messages capped at %d bytes.", maxEvents, maxMessageBytes),
		fmt.Sprintf("Logs per stream: request at most %d tail lines / %d bytes; retain %d tail lines plus at most %d error lines, each capped at %d bytes.", logTailLines, logLimitBytes, logKeepLines, logMaxErrorLines, logMaxLineBytes),
	}
}
