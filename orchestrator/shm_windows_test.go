package main

import (
	"bytes"
	"testing"
)

func TestSharedMemory(t *testing.T) {
	name := "Local\\TestSHM123"
	size := uint32(1024)

	shm, err := NewSharedMemory(name, size)
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}
	defer shm.Close()

	testData := []byte("hello shared memory")
	if err := shm.Write(testData); err != nil {
		t.Fatalf("Failed to write to shared memory: %v", err)
	}

	if !bytes.Equal(shm.data[:len(testData)], testData) {
		t.Fatalf("Data mismatch. Got: %s", string(shm.data[:len(testData)]))
	}

	if err := shm.Freeze(); err != nil {
		t.Fatalf("Failed to freeze shared memory: %v", err)
	}

}
