package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	hex "github.com/crazycatviking/hex/server"
)

func readObject(t *testing.T, reader io.ReadCloser) []byte {
	t.Helper()
	data, readError := io.ReadAll(reader)
	closeError := reader.Close()
	if readError != nil || closeError != nil {
		t.Fatalf("read object: %v; close object: %v", readError, closeError)
	}
	return data
}

func TestObjectReplacementAndReaderSnapshots(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	payload := []byte{0, 255, 128}
	if err := store.Put(ctx, "demo/file", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	payload[0] = 1
	reader, err := store.Open(ctx, "demo/file")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Put(ctx, "demo/file", strings.NewReader("replacement")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readObject(t, reader), []byte{0, 255, 128}) {
		t.Fatal("an existing reader changed when its object was replaced")
	}
	reader, err = store.Open(ctx, "demo/file")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "demo/file"); err != nil {
		t.Fatal(err)
	}
	if string(readObject(t, reader)) != "replacement" {
		t.Fatal("an existing reader changed when its object was deleted")
	}
	if _, err := store.Open(ctx, "demo/file"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := store.Delete(ctx, "demo/file"); !errors.Is(err, hex.ErrNotFound) {
		t.Fatalf("expected not found on deletion, got %v", err)
	}
}

func TestFailedWriteKeepsObjectAndListingIsScoped(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	for _, key := range []string{"demo/b", "demo/a", "other/a"} {
		if err := store.Put(ctx, key, strings.NewReader("saved")); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("source failed")
	if err := store.Put(ctx, "demo/a", iotest.ErrReader(failure)); !errors.Is(err, failure) {
		t.Fatalf("expected source error, got %v", err)
	}
	objects, err := store.List(ctx, "demo/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Key != "demo/a" || objects[0].Size != 5 || objects[1].Key != "demo/b" {
		t.Fatalf("unexpected listing: %v", objects)
	}
	objects, err = NewStore().List(ctx, "")
	if err != nil || objects == nil || len(objects) != 0 {
		t.Fatalf("a new store must be empty: %v, %v", objects, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(cancelled, "demo/a", strings.NewReader("changed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestConcurrentObjectAccess(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			key := fmt.Sprintf("demo/%d", i)
			if err := store.Put(ctx, key, strings.NewReader("data")); err != nil {
				t.Error(err)
			}
			if _, err := store.List(ctx, "demo/"); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	objects, err := store.List(ctx, "demo/")
	if err != nil || len(objects) != 32 {
		t.Fatalf("unexpected final listing: %v, %v", objects, err)
	}
}
