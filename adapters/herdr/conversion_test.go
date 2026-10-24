package herdr_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
)

func TestMain(m *testing.M) {
	update := flag.Bool("update", false, "regenerate conversion goldens")
	flag.Parse()
	if *update {
		err := eachGolden(func(path string, data []byte, platform string) error {
			doc, problems, notes := manifest.Convert(data, manifest.Options{Dir: "/plugins/" + filepath.Base(filepath.Dir(path)), Executable: "/opt/husk-herdr", Platform: platform})
			if doc == nil {
				return fmt.Errorf("%s: conversion failed", path)
			}
			output, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), platform+".golden.json"), append(output, '\n'), 0600); err != nil {
				return err
			}
			var diagnostics bytes.Buffer
			if err := manifest.WriteDiagnostics(&diagnostics, problems, notes); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(filepath.Dir(path), platform+".problems.golden"), diagnostics.Bytes(), 0600)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func eachGolden(visit func(string, []byte, string) error) error {
	paths, err := filepath.Glob("testdata/manifests/*/herdr-plugin.toml")
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no manifest fixtures")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, platform := range []string{"linux", "darwin", "windows"} {
			if err := visit(path, data, platform); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestConversionGoldens(t *testing.T) {
	err := eachGolden(func(path string, data []byte, platform string) error {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+platform, func(t *testing.T) {
			original := bytes.Clone(data)
			doc, problems, notes := manifest.Convert(data, manifest.Options{Dir: "/plugins/" + filepath.Base(filepath.Dir(path)), Executable: "/opt/husk-herdr", Platform: platform})
			if doc == nil {
				t.Fatalf("conversion failed: %+v", problems)
			}
			output, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join(filepath.Dir(path), platform+".golden.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(append(output, '\n'), expected) {
				t.Fatalf("conversion differs from %s golden\ngot:\n%s", platform, output)
			}
			var diagnostics bytes.Buffer
			if err := manifest.WriteDiagnostics(&diagnostics, problems, notes); err != nil {
				t.Fatal(err)
			}
			expectedDiagnostics, err := os.ReadFile(filepath.Join(filepath.Dir(path), platform+".problems.golden"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(diagnostics.Bytes(), expectedDiagnostics) {
				t.Fatalf("stderr differs from %s golden\ngot:\n%s", platform, &diagnostics)
			}
			if !bytes.Equal(data, original) {
				t.Fatal("conversion mutated input")
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
