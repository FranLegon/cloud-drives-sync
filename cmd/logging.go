package cmd

import (
	"fmt"
)

func Log(tag string, format string, a ...interface{}) {
	fmt.Printf("[%s] "+format+"\n", append([]interface{}{tag}, a...)...)
}

func LogError(tag string, err error, format string, a ...interface{}) {
	args := append([]interface{}{tag}, a...)
	args = append(args, err)
	fmt.Printf("[ERROR][%s] "+format+": %v\n", args...)
}
