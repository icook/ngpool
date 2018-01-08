package main

import (
	"github.com/spf13/cobra"
)

func init() {
	var drop bool
	provisionCmd := &cobra.Command{
		Use: "provision",
		Run: func(cmd *cobra.Command, args []string) {
			ng := NewNgWebAPI()
			ng.ParseConfig()
			ng.ConnectDB()

			if drop {
				drop := ng.mustLoadAsset("sql/drop.sql")
				ng.mustRunSQL(drop)
			}
			tables := ng.mustLoadAsset("sql/tables.sql")
			ng.mustRunSQL(tables)
		},
	}
	provisionCmd.Flags().BoolVarP(&drop, "drop", "d", false, "whether to drop existing schemas")
	RootCmd.AddCommand(provisionCmd)
}

func (q *NgWebAPI) mustLoadAsset(asset string) []byte {
	data, err := Asset(asset)
	if err != nil {
		panic("no tables.sql to load")
	}
	return data
}
