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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

const maxRuleSourceBytes = 32 << 20

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
		refreshErr = errors.New("response contains no supported rules")
	}

	cached, cacheErr := m.readRuleCache(url)
	if cacheErr != nil {
		logx.Warnf("GeoData", "Failed to refresh %s: %v; cached rules unavailable: %v", url, refreshErr, cacheErr)
		return false
	}
	if !parseRuleSource(string(cached), state) {
		logx.Warnf("GeoData", "Failed to refresh %s: %v; cached rules are invalid", url, refreshErr)
		return false
	}
	logx.Warnf("GeoData", "Failed to refresh %s: %v; using cached rules", url, refreshErr)
	return true
}

// downloadRuleSource returns the body as a string so the rules parsed from it can alias it.
func downloadRuleSource(url string) (string, error) {
	client := newRuleDownloadClient()
	// Each download builds its own transport; do not leave its connections idling.
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	var body strings.Builder
	if n := resp.ContentLength; n > 0 && n <= maxRuleSourceBytes {
		body.Grow(int(n))
	}
	if _, err := io.Copy(&body, io.LimitReader(resp.Body, maxRuleSourceBytes+1)); err != nil {
		return "", err
	}
	if body.Len() > maxRuleSourceBytes {
		return "", fmt.Errorf("response larger than %d bytes", maxRuleSourceBytes)
	}
	if strings.TrimSpace(body.String()) == "" {
		return "", errors.New("empty response")
	}
	return body.String(), nil
}
