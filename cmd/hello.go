package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var name string

var helloCmd = &cobra.Command{
	Use:   "hello",
	Short: "Menyapa seseorang",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Halo, %s!\n", name)
	},
}

func init() {
	helloCmd.Flags().StringVarP(&name, "name", "n", "dunia", "nama yang disapa")
	rootCmd.AddCommand(helloCmd)
}
