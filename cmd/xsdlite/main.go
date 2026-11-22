// Command xsdlite validates XML documents against an XML Schema.
//
//	xsdlite schema.xsd document.xml [more.xml ...]
//
// It prints one line per violation and exits with status 1 when any document
// is not valid, or 2 when the schema or a document cannot be read.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/stevenblair/xsdlite"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: xsdlite schema.xsd document.xml [more.xml ...]")
		os.Exit(2)
	}
	start := time.Now()
	schema, err := xsdlite.Compile(os.DirFS(filepath.Dir(os.Args[1])), filepath.Base(os.Args[1]))
	if err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "schema compiled in %v\n", time.Since(start).Round(time.Microsecond))
	status := 0
	for _, name := range os.Args[2:] {
		file, err := os.Open(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 2
			continue
		}
		start = time.Now()
		result, err := schema.Validate(file, xsdlite.Options{})
		file.Close()
		if err != nil {
			fmt.Printf("%s: not well-formed: %v\n", name, err)
			status = 2
			continue
		}
		for _, v := range result.Violations {
			fmt.Printf("%s:%s\n", name, v)
		}
		verdict := "valid"
		if !result.Valid() {
			verdict = fmt.Sprintf("%d violations", len(result.Violations))
			if status == 0 {
				status = 1
			}
		}
		fmt.Fprintf(os.Stderr, "%s: %s (%v)\n", name, verdict, time.Since(start).Round(time.Microsecond))
	}
	os.Exit(status)
}
