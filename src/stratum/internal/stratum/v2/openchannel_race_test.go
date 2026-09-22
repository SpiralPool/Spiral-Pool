// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"sync"
	"testing"
)

// A job broadcast sends to every channel in a session. A new channel used to join
// its session before its OpenMiningChannel.Success was written, so a broadcast in
// that gap sent the client a job for a channel it had not been told about.
func TestSV2OpenChannel_SuccessPrecedesBroadcastJobs(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				srv.BroadcastJob(pool.currentJob())
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	for i := 0; i < 50; i++ {
		client := dialSV2(t, srv)
		client.setup()
		msg, err := EncodeOpenStandardMiningChannel(&OpenStandardMiningChannel{
			RequestID:       1,
			UserIdentity:    "minerA.rig1",
			NominalHashRate: 1e6,
		})
		if err != nil {
			t.Fatal(err)
		}
		client.send(msg)
		client.expect(MsgOpenStandardMiningChannelSuccess)
		_ = client.raw.Close()
	}
}
