//go:build modeleval

package investigation

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestEvaluateModel(t *testing.T) {
	summary, err := runModelEvaluation(context.Background(), os.Getenv, func(line []byte) error {
		_, err := fmt.Fprintln(os.Stdout, string(line))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.AutomatedPassed {
		t.Fatal("evaluation incomplete or automated checks failed; inspect every attempt")
	}
}
