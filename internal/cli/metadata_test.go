package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func createBuild(t *testing.T, project string) {
	t.Helper()
	directory := filepath.Join(project, "dist")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("test website"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedMetadataDoesNotExposeConfiguration(t *testing.T) {
	directory := t.TempDir()
	project := filepath.Join(directory, "demo")
	destination := filepath.Join(directory, "sites")
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	run(t, directory, "init", project, "--publish-root", destination)
	createBuild(t, project)
	discoverable := false
	config := Project{
		Name: "demo", Title: "Team dashboard", Description: "Daily work", Author: "Alex",
		Discoverable: &discoverable,
		Server:       "http://localhost:8080", Resource: "private-resource",
		Publishing: &Publishing{Provider: "filesystem", Root: destination},
	}
	configPath := filepath.Join(project, "hex.json")
	if err := writeJSONFile(configPath, config); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for range 2 {
		before := time.Now()
		run(t, project, "publish")
		data, err := os.ReadFile(filepath.Join(destination, "demo", ".hex-site.json"))
		if err != nil {
			t.Fatal(err)
		}
		var metadata hex.SiteMetadata
		if err := decodeStrict(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Title != config.Title || metadata.Description != config.Description || metadata.Author != config.Author {
			t.Fatalf("unexpected metadata: %+v", metadata)
		}
		if metadata.Discoverable == nil || *metadata.Discoverable {
			t.Fatal("publishing lost the discovery opt-out")
		}
		if metadata.PublishedAt.Before(before) || metadata.PublishedAt.After(time.Now()) || !metadata.PublishedAt.After(previous) {
			t.Fatalf("incorrect publication timestamp: %s", metadata.PublishedAt)
		}
		previous = metadata.PublishedAt
	}
	current, err := os.ReadFile(configPath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("publishing modified project configuration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "dist", ".hex-site.json")); !os.IsNotExist(err) {
		t.Fatalf("publishing modified build output: %v", err)
	}
}

func TestInitializationPreservesAppAndCanPinProfile(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(directory, "profiles"))
	if _, err := saveProfile(localConnection("http://localhost:8080", filepath.Join(directory, "sites")), "local"); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(directory, "demo")
	createBuild(t, project)
	run(t, directory, "init", project, "--platform", "local")
	data, err := os.ReadFile(filepath.Join(project, "hex.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config Project
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Platform != "local" || config.Directory != "" {
		t.Fatalf("unexpected project: %+v", config)
	}
	app, _ := testApp(t, project)
	resolved, err := app.commandConfig("", true)
	if err != nil || resolved.Directory != "dist" || resolved.Server != "http://localhost:8080" {
		t.Fatalf("unexpected resolved configuration: %+v, %v", resolved, err)
	}
	data, err = os.ReadFile(filepath.Join(project, "dist", "index.html"))
	if err != nil || string(data) != "test website" {
		t.Fatalf("existing app changed: %v", err)
	}
}
