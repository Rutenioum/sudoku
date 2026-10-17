/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

func TestFinalizeWizardNormalizesInput(t *testing.T) {
	dir := t.TempDir()
	res, err := finalizeWizard(wizardInput{
		Host:              " example.com ",
		ServerPort:        -1,
		MixPort:           0,
		FallbackAddr:      " 127.0.0.1:80 ",
		AEAD:              " AES-128-GCM ",
		ASCIIMode:         "bogus",
		SuspiciousAction:  " SILENT ",
		PaddingMin:        -3,
		PaddingMax:        -9,
		CustomTable:       " xpxvvpvv ",
		HTTPMaskMode:      " AUTO ",
		HTTPMaskHost:      " front.example.com ",
		HTTPMaskPathRoot:  " aabbcc ",
		HTTPMaskMultiplex: " On ",
		Key:               " secret ",
		ServerPath:        filepath.Join(dir, "server.json"),
		ClientPath:        filepath.Join(dir, "client.json"),
	})
	if err != nil {
		t.Fatalf("finalizeWizard() error = %v", err)
	}

	srv, cli := res.ServerConfig, res.ClientConfig
	if srv.LocalPort != 8080 || cli.LocalPort != 1080 || cli.ServerAddress != "example.com:8080" {
		t.Fatalf("ports/address = %d, %d, %q", srv.LocalPort, cli.LocalPort, cli.ServerAddress)
	}
	if srv.Key != "secret" || cli.Key != "secret" || srv.AEAD != "aes-128-gcm" || cli.AEAD != "aes-128-gcm" {
		t.Fatalf("key/aead = %q/%q, %q/%q", srv.Key, cli.Key, srv.AEAD, cli.AEAD)
	}
	if srv.ASCII != "prefer_entropy" || srv.SuspiciousAction != "silent" || srv.FallbackAddr != "127.0.0.1:80" {
		t.Fatalf("ascii/suspicious/fallback = %q/%q/%q", srv.ASCII, srv.SuspiciousAction, srv.FallbackAddr)
	}
	if srv.PaddingMin != 0 || srv.PaddingMax != 0 || srv.CustomTable != "xpxvvpvv" {
		t.Fatalf("padding/table = %d..%d, %q", srv.PaddingMin, srv.PaddingMax, srv.CustomTable)
	}
	wantMask := config.HTTPMaskConfig{Mode: "auto", Host: "front.example.com", PathRoot: "aabbcc", Multiplex: "on"}
	if srv.HTTPMask != wantMask || cli.HTTPMask != wantMask || srv.Multiplex != "on" {
		t.Fatalf("httpmask = %+v / %+v, multiplex %q", srv.HTTPMask, cli.HTTPMask, srv.Multiplex)
	}
	if cli.ProxyMode != "pac" || len(cli.RuleURLs) == 0 {
		t.Fatalf("client proxy mode = %q rules=%v", cli.ProxyMode, cli.RuleURLs)
	}

	for _, path := range []string{res.ServerConfigPath, res.ClientConfigPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("config not saved: %v", err)
		}
	}
	fromLink, err := config.BuildConfigFromShortLink(res.ShortLink)
	if err != nil {
		t.Fatalf("short link does not parse: %v", err)
	}
	if fromLink.ServerAddress != cli.ServerAddress || fromLink.Key != "secret" || fromLink.AEAD != "aes-128-gcm" {
		t.Fatalf("short link config = %+v", fromLink)
	}
}

func TestFinalizeWizardDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	res, err := finalizeWizard(wizardInput{PaddingMin: 10, PaddingMax: 4})
	if err != nil {
		t.Fatalf("finalizeWizard() error = %v", err)
	}
	srv, cli := res.ServerConfig, res.ClientConfig
	if res.ServerConfigPath != "config.server.json" || res.ClientConfigPath != "client.config.json" {
		t.Fatalf("default paths = %q, %q", res.ServerConfigPath, res.ClientConfigPath)
	}
	if cli.ServerAddress != "127.0.0.1:8080" || srv.AEAD != "chacha20-poly1305" {
		t.Fatalf("defaults = %q, %q", cli.ServerAddress, srv.AEAD)
	}
	if srv.PaddingMin != 10 || srv.PaddingMax != 10 {
		t.Fatalf("padding = %d..%d, want max raised to min", srv.PaddingMin, srv.PaddingMax)
	}
	if srv.Key == "" || srv.Key != cli.Key {
		t.Fatalf("generated key mismatch: %q vs %q", srv.Key, cli.Key)
	}
	if srv.SuspiciousAction != "fallback" || srv.HTTPMask.Mode != "legacy" || srv.HTTPMask.Multiplex != "off" {
		t.Fatalf("enum defaults = %q/%q/%q", srv.SuspiciousAction, srv.HTTPMask.Mode, srv.HTTPMask.Multiplex)
	}
}
