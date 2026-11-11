package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"time"
)

var errRuntimeProbe = errors.New("native runtime observation could not be completed")

const runtimeProbeTimeout = 5 * time.Second

type runtimeProbeOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (output *runtimeProbeOutput) Write(data []byte) (int, error) {
	if len(data) > (1<<20)-output.buffer.Len() {
		output.cancel()
		return 0, errRuntimeProbe
	}
	return output.buffer.Write(data)
}

// Callers supply fixed imported executables and explicit read-only arguments.
// Native output and parser errors never cross the operator/API error boundary.
func runtimeProbe(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
	defer cancel()
	process := nativeCommand(bounded, executable, arguments...)
	output := runtimeProbeOutput{cancel: cancel}
	process.Stdout = &output
	if process.Run() != nil {
		if err := bounded.Err(); err != nil {
			return nil, errors.Join(errRuntimeProbe, err)
		}
		return nil, errRuntimeProbe
	}
	return output.buffer.Bytes(), nil
}
