package app

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

type clientRuntime struct {
	Config     *config.Config
	Tables     []*sudoku.Table
	BaseDialer tunnel.BaseDialer
	Dialer     tunnel.Dialer
	NodeID     string
}

func buildClientRuntime(cfg *config.Config, tables []*sudoku.Table) (*clientRuntime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil client config")
	}
	if cfg.Mode != "client" {
		return nil, fmt.Errorf("config mode must be client, got %q", cfg.Mode)
	}
	nodeID := clientNodeID(cfg)

	privateKeyBytes, changed, err := normalizeClientKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("process key for %s: %w", nodeID, err)
	}
	if changed {
		logx.Infof("Init", "Derived Public Key for %s: %s", nodeID, cfg.Key)
	}

	if len(tables) == 0 || changed {
		tables, err = BuildTables(cfg)
		if err != nil {
			return nil, fmt.Errorf("build table(s) for %s: %w", nodeID, err)
		}
	}

	baseDialer := tunnel.BaseDialer{
		Config:     cfg,
		Tables:     tables,
		PrivateKey: privateKeyBytes,
	}

	var dialer tunnel.Dialer
	if cfg.SessionMuxEnabled() {
		dialer = &tunnel.MuxDialer{BaseDialer: baseDialer}
	} else {
		dialer = &tunnel.StandardDialer{BaseDialer: baseDialer}
	}

	return &clientRuntime{
		Config:     cfg,
		Tables:     tables,
		BaseDialer: baseDialer,
		Dialer:     dialer,
		NodeID:     nodeID,
	}, nil
}

func buildClientRuntimes(configs []*config.Config, tableSets [][]*sudoku.Table) ([]*clientRuntime, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("no client configs provided")
	}

	runtimes := make([]*clientRuntime, 0, len(configs))
	for i, cfg := range configs {
		var tables []*sudoku.Table
		if i < len(tableSets) {
			tables = tableSets[i]
		}
		rt, err := buildClientRuntime(cfg, tables)
		if err != nil {
			return nil, err
		}
		runtimes = append(runtimes, rt)
	}
	return runtimes, nil
}

func buildOutbounds(runtimes []*clientRuntime) (*outbounds, error) {
	if len(runtimes) == 0 {
		return nil, fmt.Errorf("no client runtimes provided")
	}
	names := make([]string, len(runtimes))
	muxCount := 0
	for i, rt := range runtimes {
		names[i] = cmp.Or(rt.Config.Name, rt.NodeID)
		if rt.Config.SessionMuxEnabled() {
			muxCount++
		}
	}
	if muxCount > 0 {
		logx.Infof("Init", "Enabled session mux on %d/%d outbound node(s)", muxCount, len(runtimes))
	}

	nodes := make([]outboundNode, len(runtimes))
	for i, name := range uniqueNodeNames(names) {
		nodes[i] = outboundNode{name: name, dialer: runtimes[i].Dialer}
	}
	return newOutbounds(nodes)
}

func startMuxWarmers(runtimes []*clientRuntime) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var muxDialers []*tunnel.MuxDialer

	for _, rt := range runtimes {
		muxDialer, ok := rt.Dialer.(*tunnel.MuxDialer)
		if !ok {
			continue
		}
		muxDialers = append(muxDialers, muxDialer)
		wg.Add(1)
		go func(nodeID string, dialer *tunnel.MuxDialer) {
			defer wg.Done()
			ready := false
			failureReported := false
			dialer.Maintain(ctx, func(err error) {
				if err == nil {
					if !ready {
						logx.Infof("Mux", "Warm session ready for %s", nodeID)
					}
					ready = true
					failureReported = false
					return
				}
				if !failureReported {
					logx.Warnf("Mux", "Warm session unavailable for %s: %v", nodeID, err)
				}
				ready = false
				failureReported = true
			})
		}(rt.NodeID, muxDialer)
	}

	return func() {
		cancel()
		for _, dialer := range muxDialers {
			_ = dialer.Close()
		}
		wg.Wait()
	}
}

func clientNodeID(cfg *config.Config) string {
	serverAddr := strings.TrimSpace(cfg.ServerAddress)
	if serverAddr == "" {
		serverAddr = "unknown-server"
	}
	mode := cfg.HTTPMask.Mode
	if strings.TrimSpace(mode) == "" {
		mode = "legacy"
	}
	return fmt.Sprintf("%s/%s", serverAddr, mode)
}
