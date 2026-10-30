package main

import "strings"

// argValue is the value of a flag in args, given as "--flag value" or "--flag=value": what a
// Windows service reads its data directory from before its command line is parsed, to know where
// to put its log.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
