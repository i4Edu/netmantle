package api

import (
	"net/http/cookiejar"
	"strconv"
)

// formatInt returns the decimal representation of i.
func formatInt(i int64) string { return strconv.FormatInt(i, 10) }

// cookieJarFunc returns a stdlib in-memory cookie jar; isolated to ease
// substitution in tests.
func cookieJarFunc() (*cookiejar.Jar, error) { return cookiejar.New(nil) }
