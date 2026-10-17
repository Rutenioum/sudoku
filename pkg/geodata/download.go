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
package geodata

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

var newRuleDownloadClient = func() *http.Client {
	return dnsutil.NewOutboundHTTPClient(30*time.Second, dnsutil.ClientResolver())
}

func (m *Manager) downloadAndParse(url string, state *ruleBuildState) bool {
	body, refreshErr := downloadRuleSource(url)
	if refreshErr == nil {
		if parseRuleSource(body, state) {
			if m.cacheDir != "" {
				if err := m.writeRuleCache(url, body); err != nil {
					logx.Warnf("GeoData", "Rules loaded from %s, but cache update failed: %v", url, err)
				}
			}
			return true
		}
		refreshErr = fmt.Errorf("response contains no supported rules")
	}

	body, cacheErr := m.readRuleCache(url)
	if cacheErr != nil {
		logx.Warnf("GeoData", "Failed to refresh %s: %v; cached rules unavailable: %v", url, refreshErr, cacheErr)
		return false
	}
	if !parseRuleSource(body, state) {
		logx.Warnf("GeoData", "Failed to refresh %s: %v; cached rules are invalid", url, refreshErr)
		return false
	}
	logx.Warnf("GeoData", "Failed to refresh %s: %v; using cached rules", url, refreshErr)
	return true
}

func downloadRuleSource(url string) ([]byte, error) {
	client := newRuleDownloadClient()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return body, nil
}
