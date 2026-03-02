// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package mockdaemon provides a mock Bitcoin/DigiByte/BCH daemon for testing.
// It implements the JSON-RPC interface used by the real daemons.
package mockdaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

// MockDaemon simulates a cryptocurrency daemon for testing.
type MockDaemon struct {
	mu            sync.RWMutex
	server        *httptest.Server
	blockHeight   uint64
	difficulty    float64
	networkHash   float64
	blockTemplate *BlockTemplate
	blocks        []string // Submitted blocks
	callCount     atomic.Int64
	lastCall      string
	failNext      bool
	latency       time.Duration

	// Callbacks for custom behavior
	OnGetBlockTemplate func() (*BlockTemplate, error)
	OnSubmitBlock      func(blockHex string) error
}

// BlockTemplate represents a getblocktemplate response.
type BlockTemplate struct {
	Version              int           `json:"version"`
	PreviousBlockHash    string        `json:"previousblockhash"`
	Transactions         []Transaction `json:"transactions"`
	CoinbaseAux          CoinbaseAux   `json:"coinbaseaux"`
	CoinbaseValue        int64         `json:"coinbasevalue"`
	Target               string        `json:"target"`
	MinTime              int64         `json:"mintime"`
	Mutable              []string      `json:"mutable"`
	NonceRange           string        `json:"noncerange"`
	SigOpLimit           int           `json:"sigoplimit"`
	SizeLimit            int           `json:"sizelimit"`
	WeightLimit          int           `json:"weightlimit"`
	CurrentTime          int64         `json:"curtime"`
	Bits                 string        `json:"bits"`
	Height               int64         `json:"height"`
	DefaultWitnessCommit string        `json:"default_witness_commitment,omitempty"`
}

// Transaction represents a transaction in the block template.
type Transaction struct {
	Data    string `json:"data"`
	TxID    string `json:"txid"`
	Hash    string `json:"hash"`
	Fee     int64  `json:"fee"`
	SigOps  int    `json:"sigops"`
	Weight  int    `json:"weight,omitempty"`
	Depends []int  `json:"depends,omitempty"`
}

// CoinbaseAux contains auxiliary data for coinbase.
type CoinbaseAux struct {
	Flags string `json:"flags"`
}

// BlockchainInfo represents getblockchaininfo response.
type BlockchainInfo struct {
	Chain                string  `json:"chain"`
	Blocks               uint64  `json:"blocks"`
	Headers              uint64  `json:"headers"`
	BestBlockHash        string  `json:"bestblockhash"`
	Difficulty           float64 `json:"difficulty"`
	MedianTime           int64   `json:"mediantime"`
	VerificationProgress float64 `json:"verificationprogress"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	Pruned               bool    `json:"pruned"`
}

// RPCRequest represents a JSON-RPC request.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// RPCResponse represents a JSON-RPC response.
type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// New creates a new mock daemon.
func New() *MockDaemon {
	d := &MockDaemon{
		blockHeight: 100000,
		difficulty:  1000.0,
		networkHash: 1e15,
		blocks:      make([]string, 0),
	}

	d.blockTemplate = d.generateBlockTemplate()

	d.server = httptest.NewServer(http.HandlerFunc(d.handleRPC))
	return d
}

// URL returns the mock daemon's URL.
func (d *MockDaemon) URL() string {
	return d.server.URL
}

// Close shuts down the mock daemon.
func (d *MockDaemon) Close() {
	d.server.Close()
}

// SetBlockHeight sets the current block height.
func (d *MockDaemon) SetBlockHeight(height uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.blockHeight = height
	d.blockTemplate = d.generateBlockTemplate()
}

// SetDifficulty sets the network difficulty.
func (d *MockDaemon) SetDifficulty(diff float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.difficulty = diff
}

// SetLatency sets artificial latency for responses.
func (d *MockDaemon) SetLatency(latency time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.latency = latency
}

// SetFailNext makes the next RPC call fail.
func (d *MockDaemon) SetFailNext(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failNext = fail
}

// SetSubmitBlockError configures an error to return on next submitblock.
func (d *MockDaemon) SetSubmitBlockError(errMsg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.OnSubmitBlock = func(blockHex string) error {
		return fmt.Errorf("%s", errMsg)
	}
}

// SetSubmitBlockHandler sets a custom handler for submitblock.
func (d *MockDaemon) SetSubmitBlockHandler(handler func(blockHex string) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.OnSubmitBlock = handler
}

// ClearSubmitBlockHandler removes any custom submitblock handler.
func (d *MockDaemon) ClearSubmitBlockHandler() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.OnSubmitBlock = nil
}

// CallCount returns the number of RPC calls made.
func (d *MockDaemon) CallCount() int64 {
	return d.callCount.Load()
}

// LastCall returns the last RPC method called.
func (d *MockDaemon) LastCall() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastCall
}

// SubmittedBlocks returns all submitted block hashes.
func (d *MockDaemon) SubmittedBlocks() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]string{}, d.blocks...)
}

// SimulateNewBlock simulates a new block being mined.
func (d *MockDaemon) SimulateNewBlock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.blockHeight++
	d.blockTemplate = d.generateBlockTemplate()
}

func (d *MockDaemon) generateBlockTemplate() *BlockTemplate {
	return &BlockTemplate{
		Version:           536870912, // 0x20000000
		PreviousBlockHash: fmt.Sprintf("%064x", d.blockHeight),
		Transactions:      []Transaction{},
		CoinbaseAux:       CoinbaseAux{Flags: ""},
		CoinbaseValue:     625000000, // 6.25 coins
		Target:            "00000000ffff0000000000000000000000000000000000000000000000000000",
		MinTime:           time.Now().Unix() - 60,
		Mutable:           []string{"time", "transactions", "prevblock"},
		NonceRange:        "00000000ffffffff",
		SigOpLimit:        80000,
		SizeLimit:         4000000,
		WeightLimit:       4000000,
		CurrentTime:       time.Now().Unix(),
		Bits:              "1d00ffff",
		Height:            int64(d.blockHeight) + 1,
	}
}

func (d *MockDaemon) handleRPC(w http.ResponseWriter, r *http.Request) {
	d.callCount.Add(1)

	// Apply latency
	d.mu.RLock()
	latency := d.latency
	failNext := d.failNext
	d.mu.RUnlock()

	if latency > 0 {
		time.Sleep(latency)
	}

	// Check for failure injection
	if failNext {
		d.mu.Lock()
		d.failNext = false
		d.mu.Unlock()

		d.sendError(w, nil, -32000, "Injected failure")
		return
	}

	var req RPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		d.sendError(w, nil, -32700, "Parse error")
		return
	}

	d.mu.Lock()
	d.lastCall = req.Method
	d.mu.Unlock()

	switch req.Method {
	case "getblocktemplate":
		d.handleGetBlockTemplate(w, &req)
	case "submitblock":
		d.handleSubmitBlock(w, &req)
	case "getblockchaininfo":
		d.handleGetBlockchainInfo(w, &req)
	case "getdifficulty":
		d.handleGetDifficulty(w, &req)
	case "getnetworkhashps":
		d.handleGetNetworkHashPS(w, &req)
	case "getblockhash":
		d.handleGetBlockHash(w, &req)
	case "validateaddress":
		d.handleValidateAddress(w, &req)
	case "getmininginfo":
		d.handleGetMiningInfo(w, &req)
	default:
		d.sendError(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

func (d *MockDaemon) handleGetBlockTemplate(w http.ResponseWriter, req *RPCRequest) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.OnGetBlockTemplate != nil {
		template, err := d.OnGetBlockTemplate()
		if err != nil {
			d.sendError(w, req.ID, -32000, err.Error())
			return
		}
		d.sendResult(w, req.ID, template)
		return
	}

	d.sendResult(w, req.ID, d.blockTemplate)
}

func (d *MockDaemon) handleSubmitBlock(w http.ResponseWriter, req *RPCRequest) {
	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
		d.sendError(w, req.ID, -32602, "Invalid params")
		return
	}

	blockHex := params[0]

	if d.OnSubmitBlock != nil {
		if err := d.OnSubmitBlock(blockHex); err != nil {
			d.sendError(w, req.ID, -32000, err.Error())
			return
		}
	}

	d.mu.Lock()
	d.blocks = append(d.blocks, blockHex)
	d.blockHeight++
	d.blockTemplate = d.generateBlockTemplate()
	d.mu.Unlock()

	// submitblock returns null on success
	d.sendResult(w, req.ID, nil)
}

func (d *MockDaemon) handleGetBlockchainInfo(w http.ResponseWriter, req *RPCRequest) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info := BlockchainInfo{
		Chain:                "main",
		Blocks:               d.blockHeight,
		Headers:              d.blockHeight,
		BestBlockHash:        fmt.Sprintf("%064x", d.blockHeight),
		Difficulty:           d.difficulty,
		MedianTime:           time.Now().Unix() - 300,
		VerificationProgress: 1.0,
		InitialBlockDownload: false,
		Pruned:               false,
	}

	d.sendResult(w, req.ID, info)
}

func (d *MockDaemon) handleGetDifficulty(w http.ResponseWriter, req *RPCRequest) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.sendResult(w, req.ID, d.difficulty)
}

func (d *MockDaemon) handleGetNetworkHashPS(w http.ResponseWriter, req *RPCRequest) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.sendResult(w, req.ID, d.networkHash)
}

func (d *MockDaemon) handleGetBlockHash(w http.ResponseWriter, req *RPCRequest) {
	var params []int64
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
		d.sendError(w, req.ID, -32602, "Invalid params")
		return
	}

	height := params[0]
	hash := fmt.Sprintf("%064x", height)
	d.sendResult(w, req.ID, hash)
}

func (d *MockDaemon) handleValidateAddress(w http.ResponseWriter, req *RPCRequest) {
	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
		d.sendError(w, req.ID, -32602, "Invalid params")
		return
	}

	// For testing, accept any address that's not empty
	address := params[0]
	isValid := len(address) > 10

	result := map[string]interface{}{
		"isvalid": isValid,
		"address": address,
	}

	d.sendResult(w, req.ID, result)
}

func (d *MockDaemon) handleGetMiningInfo(w http.ResponseWriter, req *RPCRequest) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info := map[string]interface{}{
		"blocks":        d.blockHeight,
		"difficulty":    d.difficulty,
		"networkhashps": d.networkHash,
		"pooledtx":      0,
		"chain":         "main",
		"warnings":      "",
	}

	d.sendResult(w, req.ID, info)
}

func (d *MockDaemon) sendResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) // #nosec G104
}

func (d *MockDaemon) sendError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) // #nosec G104
}

// HealthyDaemon returns a daemon configured as healthy.
func HealthyDaemon() *MockDaemon {
	return New()
}

// UnhealthyDaemon returns a daemon that fails requests.
func UnhealthyDaemon() *MockDaemon {
	d := New()
	d.OnGetBlockTemplate = func() (*BlockTemplate, error) {
		return nil, fmt.Errorf("node is syncing")
	}
	return d
}

// SlowDaemon returns a daemon with high latency.
func SlowDaemon(latency time.Duration) *MockDaemon {
	d := New()
	d.SetLatency(latency)
	return d
}

// WaitForCalls waits until the daemon has received at least n calls.
func (d *MockDaemon) WaitForCalls(ctx context.Context, n int64) error {
	for {
		if d.CallCount() >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
