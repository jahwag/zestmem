//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadOptionalSecretBoundsSecretFileRead(t *testing.T) {
	const maxSecretBytes = 4096

	secretPath := filepath.Join(t.TempDir(), "streaming-secret")
	if err := syscall.Mkfifo(secretPath, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_CLIENT_SECRET", "")
	t.Setenv("TEST_CLIENT_SECRET_FILE", secretPath)

	releaseWriter := make(chan struct{})
	writerWrote := make(chan error, 1)
	writerDone := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(secretPath, os.O_WRONLY, 0)
		if err != nil {
			writerWrote <- err
			writerDone <- err
			return
		}
		_, err = file.WriteString(strings.Repeat("s", maxSecretBytes+3))
		writerWrote <- err
		if err == nil {
			<-releaseWriter
		}
		writerDone <- file.Close()
	}()

	readDone := make(chan error, 1)
	go func() {
		_, err := loadOptionalSecret("TEST_CLIENT_SECRET", "TEST_CLIENT_SECRET_FILE")
		readDone <- err
	}()

	if err := <-writerWrote; err != nil {
		close(releaseWriter)
		t.Fatalf("write streaming secret: %v", err)
	}

	select {
	case err := <-readDone:
		close(releaseWriter)
		if closeErr := <-writerDone; closeErr != nil {
			t.Fatalf("close streaming secret: %v", closeErr)
		}
		if err == nil {
			t.Fatal("oversized streaming secret was accepted")
		}
	case <-time.After(time.Second):
		close(releaseWriter)
		<-writerDone
		<-readDone
		t.Fatal("secret loader waited for EOF after reading beyond the size limit")
	}
}
