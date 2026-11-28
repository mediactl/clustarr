/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

// Package usenetaccount names a usenet provider account in the connection
// ledger and reads its quota count. It is the part of the ledger the
// manager shares with the engines: the manager reports a metered
// provider's use in DownloadClient status without linking the usenet
// client (pkg/download/usenet is the engines' alone).
package usenetaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// Key is an account's key in the ledger. For a provider on its own it is
// host, port and username: every pod and every DownloadClient configured
// with the same account shares one budget (spec 2026-10-16 §3.1), because
// the plan's cap is the account's, not the pod's. For a provider in a pool
// it is the pool's name and the username, so every host of the pool shares
// one budget too (spec 2026-11-27 §4.2).
func Key(pool, host string, port int, username string) string {
	if pool != "" {
		return "pool:" + pool + ":" + username
	}
	return host + ":" + strconv.Itoa(port) + ":" + username
}

// KeyOf is [Key] for a provider as its DownloadClient declares it, with
// the defaults the engine applies: the host trimmed, the port 563 with TLS
// and 119 without. A pool that us does not declare is no pool.
func KeyOf(us *downloadv1alpha1.UsenetSpec, p downloadv1alpha1.NNTPProvider, username string) string {
	port := int(p.Port)
	if port == 0 {
		port = 563
		if p.TLS != nil && !*p.TLS {
			port = 119
		}
	}
	pool := ""
	for _, cp := range us.ConnectionPools {
		if cp.Name == p.Pool {
			pool = p.Pool
		}
	}
	return Key(pool, strings.TrimSpace(p.Host), port, username)
}

// Usage is what every pod has read from an account in the current quota
// period.
type Usage struct {
	PeriodStart time.Time `json:"periodStart"`
	Bytes       int64     `json:"bytes"`
}

// ReadUsage reads account's quota count from usage (clustarr-usenet-usage)
// without changing it: the zero Usage when nothing has been counted.
func ReadUsage(ctx context.Context, usage events.KV, account string) (Usage, error) {
	e, err := usage.Get(ctx, events.UsenetUsageKey(account))
	if errors.Is(err, events.ErrKeyNotFound) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("usenetaccount: read usage of an account: %w", err)
	}
	var u Usage
	if len(e.Value) > 0 {
		if err := json.Unmarshal(e.Value, &u); err != nil {
			return Usage{}, fmt.Errorf("usenetaccount: decode usage of an account: %w", err)
		}
	}
	return u, nil
}
