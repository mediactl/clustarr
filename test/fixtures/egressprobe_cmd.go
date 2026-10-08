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

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// newEgressProbeCommand is the e2e suite's proof that config/e2e's
// NetworkPolicy is enforced (test/e2e/egress_test.go): run as a pod in the
// e2e namespace, it exits 0 only when it connects to every --reach address
// (so a probe that cannot connect at all never passes) and to none of the
// --refuse addresses. A refused address may fail any way -- a drop timing
// out, a reset, a failed lookup -- since none of them reached the network.
func newEgressProbeCommand() *cobra.Command {
	var (
		reach, refuse []string
		timeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "egress-probe",
		Short: "Exit 0 only if every --reach address connects and no --refuse address does",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return egressProbe(cmd.OutOrStdout(), reach, refuse, timeout)
		},
	}
	cmd.Flags().StringSliceVar(&reach, "reach", nil, "host:port addresses that must connect (in-cluster)")
	cmd.Flags().StringSliceVar(&refuse, "refuse", nil, "host:port addresses that must not connect (the Internet)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "how long one connection attempt may take")
	return cmd
}

func egressProbe(out io.Writer, reach, refuse []string, timeout time.Duration) error {
	dial := func(addr string) error {
		c, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return err
		}
		return c.Close()
	}
	var bad []string
	for _, addr := range reach {
		if err := dial(addr); err != nil {
			bad = append(bad, fmt.Sprintf("%s did not connect: %v", addr, err))
			continue
		}
		_, _ = fmt.Fprintf(out, "reached %s\n", addr)
	}
	for _, addr := range refuse {
		err := dial(addr)
		if err == nil {
			bad = append(bad, fmt.Sprintf("%s connected: the pod reaches the Internet", addr))
			continue
		}
		_, _ = fmt.Fprintf(out, "refused %s: %v\n", addr, err)
	}
	if len(bad) > 0 {
		for _, b := range bad {
			_, _ = fmt.Fprintln(os.Stderr, b)
		}
		return fmt.Errorf("egress-probe: %d of %d checks failed", len(bad), len(reach)+len(refuse))
	}
	return nil
}
