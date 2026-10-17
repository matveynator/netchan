// Command crosscompile checks each target independently, preserving platforms
// whose radio SDK only implements a subset of the available transports.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type build struct {
	name, compiler         string
	arguments, environment []string
}
type result struct {
	name   string
	output []byte
	err    error
}

func main() {
	tinygo := flag.String("tinygo", "", "also check boards with this TinyGo executable")
	flag.Parse()
	output, err := os.MkdirTemp("", "netchan-crosscompile-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(output)
	var builds []build
	for _, system := range []string{"darwin", "linux", "windows", "freebsd", "openbsd"} {
		for _, architecture := range []string{"amd64", "arm64"} {
			builds = append(builds, build{name: system + "/" + architecture, compiler: "go", arguments: []string{"build", "./..."}, environment: []string{"CGO_ENABLED=0", "GOOS=" + system, "GOARCH=" + architecture}})
		}
	}
	for _, tags := range []string{"netchan_no_quic", "netchan_no_ble,netchan_no_rfcomm", "netchan_no_tcp,netchan_no_quic", "netchan_no_tcp,netchan_no_quic,netchan_no_ble,netchan_no_rfcomm"} {
		builds = append(builds, build{name: tags, compiler: "go", arguments: []string{"build", "-tags=" + tags, "."}, environment: []string{"CGO_ENABLED=0"}})
	}
	if *tinygo != "" {
		for _, board := range []string{"nano-33-ble-s140v7", "esp32c3-supermini", "esp32-generic", "pico-w"} {
			builds = append(builds, build{name: "tinygo/" + board, compiler: *tinygo, arguments: []string{"build", "-target=" + board, "-tags=netchan_buildcheck", "-o", filepath.Join(output, board+".elf"), "./scripts/crosscompile/fixture"}})
		}
	}
	jobs, results := make(chan build), make(chan result)
	for worker := 0; worker < 2; worker++ {
		go func() {
			for job := range jobs {
				command := exec.Command(job.compiler, job.arguments...) // #nosec G204 -- Local build tool: compiler is an explicit operator CLI choice, arguments are constructed above without network input or a shell.
				command.Env = environment(job.environment)
				output, err := command.CombinedOutput()
				results <- result{name: job.name, output: output, err: err}
			}
		}()
	}
	go func() {
		for _, job := range builds {
			jobs <- job
		}
		close(jobs)
	}()
	failed := false
	for range builds {
		completed := <-results
		if completed.err != nil {
			failed = true
			fmt.Printf("FAIL %s: %v\n%s", completed.name, completed.err, completed.output)
		} else {
			fmt.Printf("PASS %s\n", completed.name)
		}
	}
	if failed {
		os.Exit(1)
	}
}

func environment(overrides []string) []string {
	var configured []string
	for _, original := range os.Environ() {
		name, _, _ := strings.Cut(original, "=")
		replaced := false
		for _, override := range overrides {
			key, _, _ := strings.Cut(override, "=")
			if name == key {
				replaced = true
				break
			}
		}
		if !replaced {
			configured = append(configured, original)
		}
	}
	return append(configured, overrides...)
}
