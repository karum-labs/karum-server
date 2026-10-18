package geocode

import "strconv"

func strconvFormat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func parseFloatImpl(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
