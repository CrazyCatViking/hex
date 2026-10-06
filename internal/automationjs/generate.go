//go:build ignore

// Rebuild the embedded engine with WASI SDK 27. No C toolchain is needed to
// build or deploy Hex itself; the resulting Wasm artifact is checked in.
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build() error {
	sdk := os.Getenv("WASI_SDK_PATH")
	if sdk == "" {
		return fmt.Errorf("set WASI_SDK_PATH to a WASI SDK 27 installation")
	}
	directory, err := os.MkdirTemp("", "hex-quickjs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	response, err := http.Get("https://codeload.github.com/quickjs-ng/quickjs/tar.gz/6d46d07d04041b40f4f49eaa7fdebe44c314c699")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download QuickJS: %s", response.Status)
	}
	compressed, err := gzip.NewReader(response.Body)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Only engine sources and headers from the archive's root are needed.
		parts := strings.Split(header.Name, "/")
		if len(parts) != 2 || header.Typeflag != tar.TypeReg {
			continue
		}
		name := parts[1]
		if filepath.Ext(name) != ".c" && filepath.Ext(name) != ".h" {
			continue
		}
		file, err := os.Create(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, archive)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	arguments := []string{"-O2", "-DNDEBUG", "-D_GNU_SOURCE", "-D_WASI_EMULATED_SIGNAL", "-funsigned-char", "-mexec-model=reactor", "-I", directory,
		"engine/bridge.c", filepath.Join(directory, "quickjs.c"), filepath.Join(directory, "dtoa.c"), filepath.Join(directory, "libregexp.c"), filepath.Join(directory, "libunicode.c"),
		"-lm", "-lwasi-emulated-signal", "-Wl,-z,stack-size=2097152", "-Wl,--max-memory=67108864", "-Wl,--strip-all", "-o", "engine.wasm"}
	for _, name := range []string{"initialize", "validate_script", "run_script", "result_ptr", "result_len", "malloc", "free"} {
		arguments = append(arguments, "-Wl,--export="+name)
	}
	command := exec.Command(filepath.Join(sdk, "bin", "clang"), arguments...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}
