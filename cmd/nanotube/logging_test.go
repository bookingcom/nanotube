package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestTerminalAccountingBypassesSampling(t *testing.T) {
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.InfoLevel)
	lg := zap.New(shutdownCore{Core: zapcore.NewSamplerWithOptions(core, time.Hour, 10, 1000), terminal: core}).With(zap.String("context", "preserved"))
	for i := 0; i < 12; i++ {
		lg.Info("ordinary runtime log")
		lg.Info("Shutdown: TCP target summary", zap.Int("target", i))
	}
	lg.Info("Shutdown: TCP output summary")
	counts := map[string]int{}
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		if entry["context"] != "preserved" {
			t.Fatal("terminal logger lost bound fields")
		}
		counts[entry["msg"].(string)]++
	}
	if counts["ordinary runtime log"] != 10 || counts["Shutdown: TCP target summary"] != 12 || counts["Shutdown: TCP output summary"] != 1 {
		t.Fatalf("unexpected log counts: %v", counts)
	}
}
