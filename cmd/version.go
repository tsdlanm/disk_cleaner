package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version diisi saat build: go build -ldflags "-X github.com/tsdlanm/cli_app/cmd.Version=1.0.0"
var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Menampilkan versi aplikasi",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
