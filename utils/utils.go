package utils

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"

	"github.com/sirupsen/logrus"
)

func Myprint(data interface{}, msg string) {
	ans, _ := json.Marshal(data)
	logrus.Infof(msg+"  %s", string(ans))
}

var RE_stripFnPreamble = regexp.MustCompile(`^.*\.(.*)$`)

// Trace Functions
func Enter() string {
	fnName := "<unknown>"
	// Skip this function, and fetch the PC and file for its parent
	pc, _, _, ok := runtime.Caller(1)
	if ok {
		fnName = RE_stripFnPreamble.ReplaceAllString(runtime.FuncForPC(pc).Name(), "$1")
	}

	fmt.Printf("Entering %s\n", fnName)
	return fnName
}

func Exit(s string) {
	fmt.Printf("Exiting  %s\n", s)
}
