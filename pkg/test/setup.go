package test

import (
	"bufio"
	"os"
	"path/filepath"

	"github.com/bookingcom/nanotube/pkg/conf"
	"github.com/bookingcom/nanotube/pkg/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// Setup a basic system for testing. Reads and returns test data.
func Setup() (data [][]byte, ms *metrics.Prom, lg *zap.Logger, errRet error) {
	fixturesPath := "../test/"

	in, err := os.Open(filepath.Join(fixturesPath, "in"))
	if err != nil {
		errRet = errors.Wrap(err, "error opening the in data file")
		return
	}
	defer func() {
		err := in.Close()
		if err != nil {
			errRet = errors.Wrap(err, "error closing in data test file")
			return
		}
	}()

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		token := scanner.Bytes()
		rec := make([]byte, len(token))
		copy(rec, token)
		data = append(data, rec)
	}

	if err := scanner.Err(); err != nil {
		errRet = errors.Wrap(err, "error while scan-reading the sample in metrics")
		return
	}

	lg, _ = zap.NewProduction()

	cfg := conf.MakeDefault()
	ms = metrics.New(&cfg)

	return
}
