// Command memhog holds MEM_MIB mebibytes of resident memory, so its memory
// limit decides whether it runs or is OOM-killed. Used by the e2e test.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	mib, err := strconv.Atoi(os.Getenv("MEM_MIB"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "MEM_MIB must be an integer")
		os.Exit(2)
	}
	buf := make([]byte, mib<<20)
	for {
		for i := 0; i < len(buf); i += 4096 { // touch every page so it stays resident
			buf[i]++
		}
		fmt.Printf("holding %d MiB\n", mib)
		time.Sleep(10 * time.Second)
	}
}
