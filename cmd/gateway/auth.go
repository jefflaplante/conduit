package main

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/auth/oauthflow"

	"github.com/spf13/cobra"
)

// AuthRootCmd returns the top-level "auth" command group for OAuth token management.
func AuthRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage OAuth authentication for AI providers",
		Long:  `Authenticate with AI providers using OAuth. Tokens are stored in ~/.conduit/auth.json.`,
	}

	cmd.AddCommand(authLoginCmd())
	cmd.AddCommand(authStatusCmd())
	cmd.AddCommand(authLogoutCmd())
	cmd.AddCommand(authRefreshCmd())

	return cmd
}

func authLoginCmd() *cobra.Command {
	var (
		provider  string
		clientID  string
		port      int
		noBrowser bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with an AI provider via OAuth",
		Long: `Start an OAuth Authorization Code + PKCE flow to authenticate with an AI provider.
Opens your browser to complete authentication, then stores the token locally.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider != "anthropic" {
				return fmt.Errorf("unsupported provider: %s (only 'anthropic' is supported)", provider)
			}

			// Check for existing token.
			existing, _ := oauthflow.LoadProviderToken(provider)
			if existing != nil && !existing.IsExpired() {
				fmt.Printf("You already have a valid %s token (expires %s).\n",
					provider, time.Unix(existing.ExpiresAt, 0).Format(time.RFC3339))
				fmt.Println("Run 'gateway auth logout' first to re-authenticate.")
				return nil
			}

			endpoints := oauthflow.DefaultEndpoints()

			opts := oauthflow.FlowOptions{
				ClientID:      clientID,
				PreferredPort: port,
				NoBrowser:     noBrowser,
				Endpoints:     endpoints,
			}

			token, err := oauthflow.RunLogin(context.Background(), opts)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			path, _ := oauthflow.StoragePath()
			fmt.Println()
			fmt.Println("Authentication successful!")
			fmt.Printf("  Provider:   %s\n", provider)
			fmt.Printf("  Token type: %s\n", token.TokenType)
			fmt.Printf("  Scope:      %s\n", token.Scope)
			fmt.Printf("  Expires:    %s\n", time.Unix(token.ExpiresAt, 0).Format(time.RFC3339))
			fmt.Printf("  Stored in:  %s\n", path)

			if token.RefreshToken != "" {
				fmt.Println("  Refresh:    available (token will auto-refresh)")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "anthropic", "AI provider to authenticate with")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client ID (overrides ANTHROPIC_OAUTH_CLIENT_ID env var)")
	cmd.Flags().IntVar(&port, "port", 0, "preferred callback port (default: auto-select 8085-8095)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print URL instead of opening browser (for headless/SSH)")

	return cmd
}

func authStatusCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show authentication status for an AI provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := oauthflow.LoadProviderToken(provider)
			if err != nil {
				return fmt.Errorf("failed to load token: %w", err)
			}

			path, _ := oauthflow.StoragePath()

			if token == nil {
				fmt.Printf("Not authenticated with %s.\n", provider)
				fmt.Printf("Run 'gateway auth login --provider %s' to authenticate.\n", provider)
				return nil
			}

			fmt.Printf("Provider:     %s\n", provider)
			fmt.Printf("Token type:   %s\n", token.TokenType)
			fmt.Printf("Scope:        %s\n", token.Scope)
			fmt.Printf("Obtained:     %s\n", time.Unix(token.ObtainedAt, 0).Format(time.RFC3339))
			fmt.Printf("Expires:      %s\n", time.Unix(token.ExpiresAt, 0).Format(time.RFC3339))
			fmt.Printf("Stored in:    %s\n", path)

			if token.IsExpired() {
				fmt.Println("Status:       EXPIRED")
				if token.RefreshToken != "" {
					fmt.Println("              Run 'gateway auth refresh' to get a new token.")
				} else {
					fmt.Println("              Run 'gateway auth login' to re-authenticate.")
				}
			} else {
				remaining := token.ExpiresIn()
				fmt.Printf("Status:       valid (%s remaining)\n", remaining.Round(time.Second))
			}

			if token.RefreshToken != "" {
				fmt.Println("Refresh:      available")
			} else {
				fmt.Println("Refresh:      not available")
			}

			if token.ClientID != "" {
				// Show only first 12 chars of client ID.
				display := token.ClientID
				if len(display) > 12 {
					display = display[:12] + "..."
				}
				fmt.Printf("Client ID:    %s\n", display)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "anthropic", "AI provider to check")

	return cmd
}

func authLogoutCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored OAuth token for an AI provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := oauthflow.LoadProviderToken(provider)
			if err != nil {
				return fmt.Errorf("failed to load token: %w", err)
			}
			if token == nil {
				fmt.Printf("No stored token for %s.\n", provider)
				return nil
			}

			if err := oauthflow.DeleteProviderToken(provider); err != nil {
				return fmt.Errorf("failed to remove token: %w", err)
			}

			fmt.Printf("Removed stored token for %s.\n", provider)
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "anthropic", "AI provider to log out of")

	return cmd
}

func authRefreshCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh the stored OAuth token",
		RunE: func(cmd *cobra.Command, args []string) error {
			existing, err := oauthflow.LoadProviderToken(provider)
			if err != nil {
				return fmt.Errorf("failed to load token: %w", err)
			}
			if existing == nil {
				return fmt.Errorf("no stored token for %s — run 'gateway auth login' first", provider)
			}
			if existing.RefreshToken == "" {
				return fmt.Errorf("no refresh token available — run 'gateway auth login' to re-authenticate")
			}

			newToken, err := oauthflow.RefreshToken(existing.RefreshToken, existing.ClientID)
			if err != nil {
				return fmt.Errorf("refresh failed: %w", err)
			}

			if err := oauthflow.SaveProviderToken(provider, newToken); err != nil {
				return fmt.Errorf("failed to save refreshed token: %w", err)
			}

			fmt.Println("Token refreshed successfully!")
			fmt.Printf("  New expiry: %s\n", time.Unix(newToken.ExpiresAt, 0).Format(time.RFC3339))

			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "anthropic", "AI provider to refresh token for")

	return cmd
}
