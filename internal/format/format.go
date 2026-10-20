// Package format memformat angka agar mudah dibaca.
package format

import (
	"fmt"
	"strconv"
)

var units = []string{"B", "KB", "MB", "GB", "TB", "PB"}

// Bytes memformat ukuran dalam satuan terbesar yang sesuai (basis 1024),
// misalnya 1536 menjadi "1.5 KB".
func Bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	// Hindari "1024.0 KB" akibat pembulatan.
	if v >= 1023.95 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// Int memformat bilangan bulat dengan pemisah ribuan titik, misalnya "1.204.311".
func Int(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
