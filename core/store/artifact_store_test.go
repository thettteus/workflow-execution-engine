package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/store"
)

func artifactCount(t *testing.T, base string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatalf("read artifacts dir: %v", err)
	}
	return len(entries)
}

func TestPutDedupesIdenticalContent(t *testing.T) {
	base := t.TempDir()
	s := store.New(base)
	content := []byte("hello artifact")

	h1, err := s.Put(content)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put(content)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical content produced different hashes: %s vs %s", h1, h2)
	}
	if n := artifactCount(t, base); n != 1 {
		t.Fatalf("expected exactly 1 file after two identical writes, got %d", n)
	}
}

func TestGetRoundTrip(t *testing.T) {
	s := store.New(t.TempDir())
	content := []byte("some bytes\x00\x01 binary safe")

	h, err := s.Put(content)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
	if !s.Has(h) {
		t.Error("Has should report true for a stored artifact")
	}
}

func TestGetMissingIsError(t *testing.T) {
	s := store.New(t.TempDir())
	if _, err := s.Get("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Error("expected an error for a missing artifact")
	}
	if s.Has("nope") {
		t.Error("Has should report false for a missing artifact")
	}
}

func TestDistinctContentDistinctFiles(t *testing.T) {
	base := t.TempDir()
	s := store.New(base)
	if _, err := s.Put([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put([]byte("b")); err != nil {
		t.Fatal(err)
	}
	if n := artifactCount(t, base); n != 2 {
		t.Fatalf("expected 2 files for 2 distinct contents, got %d", n)
	}
}

func TestConcurrentPutSameContentYieldsOneFile(t *testing.T) {
	base := t.TempDir()
	s := store.New(base)
	content := []byte("raced content")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Put(content); err != nil {
				t.Errorf("concurrent Put: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := artifactCount(t, base); n != 1 {
		t.Fatalf("expected exactly 1 file after concurrent identical Puts, got %d", n)
	}
}

func TestPutRejectsArtifactOverLimitWithSummary(t *testing.T) {
	s := store.NewWithOptions(t.TempDir(), store.WithMaxArtifactBytes(5), store.WithMaxTotalBytes(0))
	_, err := s.Put([]byte("hello world"))
	var limit *store.LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %T %v, want LimitError", err, err)
	}
	if limit.Code != "artifact_too_large" || limit.Limit != 5 || limit.Summary == "" {
		t.Fatalf("limit = %+v", limit)
	}
}

func TestPutRejectsStoreQuota(t *testing.T) {
	s := store.NewWithOptions(t.TempDir(), store.WithMaxArtifactBytes(0), store.WithMaxTotalBytes(6))
	if _, err := s.Put([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Put([]byte("5678"))
	var limit *store.LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %T %v, want LimitError", err, err)
	}
	if limit.Code != "artifact_quota_exceeded" {
		t.Fatalf("code = %q", limit.Code)
	}
}

func TestGarbageCollectRemovesUnreferencedArtifacts(t *testing.T) {
	base := t.TempDir()
	s := store.New(base)
	keep, err := s.Put([]byte("keep"))
	if err != nil {
		t.Fatal(err)
	}
	drop, err := s.Put([]byte("drop"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.GarbageCollect(map[string]bool{keep: true}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.RemovedFiles != 1 || stats.KeptFiles != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if !s.Has(keep) {
		t.Fatalf("kept artifact disappeared")
	}
	if s.Has(drop) {
		t.Fatalf("unreferenced artifact still exists")
	}
}
