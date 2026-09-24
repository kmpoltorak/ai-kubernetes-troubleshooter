package diagnostics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// Log limits: the API server returns at most logTailLines lines and
// logLimitBytes bytes per stream; only the last logKeepLines plus up to
// logMaxErrorLines matching error lines are kept, each capped in length.
// This bounds what is stored and sent to the LLM.
const (
	logTailLines     = 200
	logLimitBytes    = 32 << 10
	logKeepLines     = 30
	logMaxErrorLines = 20
	logMaxLineBytes  = 300
)

var (
	errorLine = regexp.MustCompile(`(?i)\b(error|err|fatal|panic|exception|failed|failure|refused|timeout|timed out|no such host|killed|denied|missing|invalid)\b`)

	logPatterns = []struct {
		re     *regexp.Regexp
		signal string
	}{
		{regexp.MustCompile(`(?i)no such host|server misbehaving|temporary failure in name resolution|lookup \S+( on \S+)?: .*(i/o timeout|no such host)|name or service not known|could not resolve host`), SignalDNSResolutionError},
		{regexp.MustCompile(`(?i)connection refused|econnrefused`), SignalConnectionRefused},
		{regexp.MustCompile(`(?i)(dial tcp [^:]+:\d+: (i/o timeout|connect: connection timed out))|connection timed out|etimedout`), SignalConnectionTimeout},
		{regexp.MustCompile(`(?i)(missing|required|invalid|unset|not set|no value for).{0,40}(config|configuration|environment variable|env var|setting)|(config|configuration).{0,40}(missing|required|invalid|not found)`), SignalConfigurationError},
		{regexp.MustCompile(`(?i)out of memory|outofmemoryerror|heap out of memory|cannot allocate memory`), SignalOutOfMemory},
	}
)

type LogStream struct {
	Container  string   `json:"container"`
	Previous   bool     `json:"previous"`
	Lines      []string `json:"lines"`
	ErrorLines []string `json:"error_lines,omitempty"`
	Truncated  bool     `json:"truncated"`
}

type LogsInfo struct {
	Pod     string      `json:"pod"`
	Streams []LogStream `json:"streams"`
}

// getPodLogs reads current and previous logs of each container in in.Names
// (or the default container when empty) of pod in.Name. A missing previous
// container is not an error.
func getPodLogs(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	if in.Name == "" {
		return Result{}, errors.New("name (pod) is required")
	}
	containers := in.Names
	if len(containers) == 0 {
		containers = []string{""}
	}
	info := LogsInfo{Pod: in.Name}
	var signals signalSet
	var errs []error
	for _, ctr := range containers {
		for _, previous := range []bool{false, true} {
			s, err := readLogs(ctx, c, in.Namespace, in.Name, ctr, previous)
			if err != nil {
				if !previous {
					errs = append(errs, fmt.Errorf("container %q: %w", ctr, err))
				}
				continue
			}
			if len(s.Lines) == 0 {
				continue
			}
			for _, line := range s.Lines {
				for _, p := range logPatterns {
					if p.re.MatchString(line) {
						signals.add(p.signal)
					}
				}
			}
			info.Streams = append(info.Streams, s)
		}
	}
	subject := "pod/" + in.Name
	if len(info.Streams) == 0 && len(errs) > 0 {
		return Result{}, errors.Join(errs...)
	}
	errorLines, lastError := 0, ""
	for _, s := range info.Streams {
		errorLines += len(s.ErrorLines)
		if n := len(s.ErrorLines); n > 0 {
			lastError = s.ErrorLines[n-1]
		}
	}
	health := domain.Healthy
	if len(signals) > 0 || errorLines > 0 {
		health = domain.Degraded
	}
	summary := fmt.Sprintf("Pod %s: %d log streams, %d error lines.", in.Name, len(info.Streams), errorLines)
	if len(signals) > 0 {
		summary += " Patterns: " + strings.Join(signals, ", ") + "."
	}
	if lastError != "" {
		summary += " Last error: " + truncate(lastError, 160)
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

func readLogs(ctx context.Context, c kube.Cluster, namespace, pod, container string, previous bool) (LogStream, error) {
	tail, limit := int64(logTailLines), int64(logLimitBytes)
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &tail, LimitBytes: &limit}
	rc, err := c.Client.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return LogStream{}, err
	}
	defer rc.Close()
	return parseLogs(io.LimitReader(rc, logLimitBytes+1), container, previous)
}

func parseLogs(r io.Reader, container string, previous bool) (LogStream, error) {
	s := LogStream{Container: container, Previous: previous}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), logLimitBytes+1)
	var all []string
	read := 0
	for sc.Scan() {
		read += len(sc.Bytes()) + 1
		line := truncate(Redact(sc.Text()), logMaxLineBytes)
		all = append(all, line)
		if errorLine.MatchString(line) && len(s.ErrorLines) < logMaxErrorLines {
			s.ErrorLines = append(s.ErrorLines, line)
		}
	}
	if err := sc.Err(); err != nil {
		return s, fmt.Errorf("read logs: %w", err)
	}
	s.Truncated = read > logLimitBytes || len(all) > logKeepLines
	if len(all) > logKeepLines {
		all = all[len(all)-logKeepLines:]
	}
	s.Lines = all
	return s, nil
}
