package conf

import "fmt"

// ValidateCompression accepts only the supported TCP wire formats.
func ValidateCompression(compression string) error {
	switch compression {
	case "", "none", "zstd":
		return nil
	default:
		return fmt.Errorf("unsupported TCP compression %q", compression)
	}
}

// ValidateTCPCompression validates global defaults, even when initially off.
func (c Main) ValidateTCPCompression() error {
	if err := ValidateCompression(c.TCPOutCompression); err != nil {
		return err
	}
	switch c.TCPOutZstdLevel {
	case "", "fastest", "default":
	default:
		return fmt.Errorf("unsupported TCPOutZstdLevel %q", c.TCPOutZstdLevel)
	}
	if c.TCPOutZstdFrameSize < 0 || c.TCPOutZstdFrameSize > 16<<20 {
		return fmt.Errorf("TCPOutZstdFrameSize must be between 1 and 16777216 (zero selects 1 MiB)")
	}
	return nil
}
