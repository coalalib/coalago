package coalago

import "fmt"

func formatByteCount(b, unit int64, prefixes, formatStr string) string {
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := unit, 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf(formatStr, float64(b)/float64(div), prefixes[exp])
}

func ByteCountBinaryBits(b int64) string {
	return formatByteCount(b*8, 1024, "KMGTPE", "%.1f %cBits")
}

func ByteCountDecimal(b int64) string {
	return formatByteCount(b, 1000, "kMGTPE", "%.1f %cB")
}

func ByteCountBinary(b int64) string {
	return formatByteCount(b, 1024, "KMGTPE", "%.1f %ciB")
}
