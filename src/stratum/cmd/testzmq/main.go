// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Mock ZMQ publisher for testing Spiral Pool's ZMQ integration
// This simulates a blockchain daemon publishing block notifications
//
// Usage:
//   go run ./cmd/testzmq
//
// This tool is for development/testing only. It publishes mock "hashblock"
// notifications on the default ZMQ port (28332) to test the pool's ZMQ
// subscriber without requiring a real blockchain daemon.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-zeromq/zmq4"
)

func main() {
	ctx := context.Background()
	pub := zmq4.NewPub(ctx)
	defer pub.Close()

	// Bind to localhost:28332 (default DigiByte ZMQ port)
	endpoint := "tcp://127.0.0.1:28332"
	if err := pub.Listen(endpoint); err != nil {
		panic(fmt.Sprintf("Failed to bind to %s: %v", endpoint, err))
	}

	fmt.Printf("Mock ZMQ Publisher started on %s\n", endpoint)
	fmt.Println("Publishing mock 'hashblock' notifications every 5 seconds...")
	fmt.Println("(Press Ctrl+C to stop)")
	fmt.Println()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	blockNum := 0
	for range ticker.C {
		blockNum++

		// Create a mock block hash (32 bytes, like a real SHA256 hash)
		mockHash := make([]byte, 32)
		copy(mockHash, []byte(fmt.Sprintf("mock-block-%08d", blockNum)))

		// Send multi-frame ZMQ message: [topic, body]
		// This matches the format used by Bitcoin/DigiByte Core
		msg := zmq4.NewMsgFrom(
			[]byte("hashblock"), // Frame 0: topic
			mockHash,            // Frame 1: block hash (32 bytes)
		)

		if err := pub.Send(msg); err != nil {
			fmt.Printf("Error sending block %d: %v\n", blockNum, err)
		} else {
			fmt.Printf("Block %d published: %x...\n", blockNum, mockHash[:8])
		}
	}
}
