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

package controller

import (
	"encoding/json"
	"net/http"
	"time"
)

// getConfigs reports the running configuration in mihomo's shape; only the mixed port and the
// mode apply to sudoku, the rest are the values that mean "off".
func (c *controller) getConfigs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"port":          0,
		"socks-port":    0,
		"redir-port":    0,
		"tproxy-port":   0,
		"mixed-port":    c.Client.MixedPort(),
		"allow-lan":     true, // the mixed listener binds every interface
		"bind-address":  "*",
		"mode":          c.Client.Mode(),
		"log-level":     "info",
		"ipv6":          true,
		"unified-delay": false,
		"tun":           map[string]any{"enable": false},
	})
}

// patchConfigs applies the mode and ignores fields sudoku cannot change at runtime.
func (c *controller) patchConfigs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode *string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Body invalid")
		return
	}
	if body.Mode != nil {
		if err := c.Client.SetMode(*body.Mode); err != nil {
			writeError(w, http.StatusBadRequest, "Body invalid")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type ruleJSON struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
	Size    int    `json:"size"`
}

func (c *controller) getRules(w http.ResponseWriter, r *http.Request) {
	rules := c.Client.Rules()
	out := make([]ruleJSON, len(rules))
	for i, rule := range rules {
		out[i] = ruleJSON{Index: i, Type: rule.Type, Payload: rule.Payload, Proxy: rule.Proxy, Size: -1}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

type ruleProviderJSON struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	VehicleType string    `json:"vehicleType"`
	Behavior    string    `json:"behavior"`
	Format      string    `json:"format"`
	RuleCount   int       `json:"ruleCount"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (c *controller) getRuleProviders(w http.ResponseWriter, r *http.Request) {
	out := make(map[string]ruleProviderJSON)
	for _, p := range c.Client.RuleProviders() {
		out[p.Name] = ruleProviderJSON{
			Name: p.Name, Type: "Rule", VehicleType: "HTTP", Behavior: "Classical", Format: "TextRule",
			RuleCount: p.RuleCount, UpdatedAt: p.UpdatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// updateRuleProvider reloads a provider in the background; the new count shows on the next read.
func (c *controller) updateRuleProvider(w http.ResponseWriter, r *http.Request) {
	if !c.Client.UpdateRuleProvider(r.PathValue("name")) {
		writeError(w, http.StatusNotFound, "Resource not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
