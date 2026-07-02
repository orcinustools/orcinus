package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/api"
)

func newAPICmd() *cobra.Command {
	var (
		addr       string
		token      string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Serve the orcinus HTTP REST API (docs/API.md)",
		Long: "Start the orcinus REST API server. Interactive docs at /docs, spec at " +
			"/openapi.json. Secure it with --token (or $ORCINUS_API_TOKEN).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				token = os.Getenv("ORCINUS_API_TOKEN")
			}
			srv := &http.Server{
				Addr:              addr,
				Handler:           api.New(api.Config{Token: token, Kubeconfig: kubeconfig}).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "orcinus api listening on %s\n", addr)
			fmt.Fprintf(out, "  docs:    http://%s/docs\n", addr)
			fmt.Fprintf(out, "  openapi: http://%s/openapi.json\n", addr)
			if token == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: no --token set — the API is unauthenticated")
			}

			// Graceful shutdown on SIGINT/SIGTERM.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errCh <- err
				}
			}()
			select {
			case err := <-errCh:
				return err
			case <-ctx.Done():
				shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(shutCtx)
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&addr, "addr", ":8080", "address to listen on")
	f.StringVar(&token, "token", "", "bearer token required for /api/v1/* (or $ORCINUS_API_TOKEN)")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, ~/.kube/config)")
	return cmd
}
