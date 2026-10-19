package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "cli_app",
	Short: "cli_app adalah aplikasi command-line berbasis Go",
	Long:  "cli_app adalah template aplikasi command-line berbasis Go dan Cobra.",
}

// Execute menjalankan root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
