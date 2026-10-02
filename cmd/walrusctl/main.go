// Command walrusctl is the operator/CI CLI. Scaffold only.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "walrusctl: schema validate|diff|apply, import, precompute, eval, bench (not implemented yet)")
	os.Exit(2)
}
