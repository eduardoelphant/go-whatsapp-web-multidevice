// Command compare reports real stable payload shapes that have no synthetic
// fixture, and paths whose JSON kind differs between real and synthetic.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eduardoelphant/go-whatsapp-web-multidevice/contract/shape"
)

func main() {
	dir := flag.String("dir", "fixtures", "fixtures root with synthetic/ and real/")
	flag.Parse()
	synthetic, err := shape.Load(filepath.Join(*dir, "synthetic"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	real, err := shape.Load(filepath.Join(*dir, "real"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report := shape.Compare(synthetic, real)
	for _, line := range report.Missing {
		fmt.Println("MISSING", line)
	}
	for _, line := range report.KindMismatches {
		fmt.Println("KIND", line)
	}
	fmt.Printf("synthetic=%d real=%d missing=%d kind_mismatches=%d\n", len(synthetic), len(real), len(report.Missing), len(report.KindMismatches))
	if len(report.Missing)+len(report.KindMismatches) > 0 {
		os.Exit(1)
	}
}
