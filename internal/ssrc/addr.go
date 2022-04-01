package ssrc

// https://tools.ietf.org/html/rfc7983
func isRTPRTCP(p []byte) bool {
	return len(p) > 0 && p[0] >= 128 && p[0] <= 191
}
