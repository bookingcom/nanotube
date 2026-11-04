package main

import (
	"strings"

	"go.uber.org/zap/zapcore"
)

// shutdownCore keeps terminal accounting outside the runtime log sampler.
// Every target can have pending output; suppressing repeated summaries loses
// the only external snapshot if the process exits before its next scrape.
type shutdownCore struct {
	zapcore.Core
	terminal zapcore.Core
}

func (c shutdownCore) With(fields []zapcore.Field) zapcore.Core {
	return shutdownCore{Core: c.Core.With(fields), terminal: c.terminal.With(fields)}
}

func (c shutdownCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if entry.Level >= zapcore.ErrorLevel && strings.HasPrefix(entry.Message, "Reload:") {
		return c.terminal.Check(entry, checked)
	}
	switch entry.Message {
	case "Shutdown: TCP target summary", "Shutdown: TCP output summary":
		return c.terminal.Check(entry, checked)
	default:
		return c.Core.Check(entry, checked)
	}
}
